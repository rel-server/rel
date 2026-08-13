package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
	"github.com/go-chi/chi"
	"github.com/k0kubun/pp"
	"sales-way.com/server/sw"
)

type samlServe struct {
	middleware  http.Handler
	login       http.Handler
	hasmetadata bool
}

func publicKey(priv interface{}) interface{} {
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		return &k.PublicKey
	case *ecdsa.PrivateKey:
		return &k.PublicKey
	default:
		return nil
	}
}

func pemBlockForKey(priv interface{}) (*pem.Block, error) {
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		return &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}, nil
	case *ecdsa.PrivateKey:
		b, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			log.Printf("Unable to marshal ECDSA private key: %v", err)
		}
		return &pem.Block{Type: "EC PRIVATE KEY", Bytes: b}, nil
	default:
		return nil, fmt.Errorf(`unable to get pem block for key`)
	}
}

func generateSelfSignedCertificate(serverNames []string) (*tls.Certificate, error) {
	// priv, err := rsa.GenerateKey(rand.Reader, *rsaBits)
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Acme Co"},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(time.Hour * 24 * 365 * 2), // valid for two years

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	// Add the DNS name of our server
	template.DNSNames = append(template.DNSNames, serverNames...)
	/*
	   hosts := strings.Split(*host, ",")
	   for _, h := range hosts {
	   	if ip := net.ParseIP(h); ip != nil {
	   		template.IPAddresses = append(template.IPAddresses, ip)
	   	} else {
	   		template.DNSNames = append(template.DNSNames, h)
	   	}
	   }
	   if *isCA {
	   	template.IsCA = true
	   	template.KeyUsage |= x509.KeyUsageCertSign
	   }
	*/

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, publicKey(priv), priv)
	if err != nil {
		// log.Fatalf("Failed to create certificate: %s", err)
		return nil, fmt.Errorf(`saml: failed to create certificate: %w`, err)
	}

	pemForKey, err := pemBlockForKey(priv)
	if err != nil {
		return nil, err
	}

	cer := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	key := pem.EncodeToMemory(pemForKey)
	cert, err := tls.X509KeyPair(cer, key)
	if err != nil {
		return nil, err
	}

	// On sauvegarde
	certout, err := os.Create("/app/saml/cert.pem")
	if err != nil {
		return nil, err
	}
	defer certout.Close()
	if _, err = certout.Write(cer); err != nil {
		return nil, err
	}

	keyout, err := os.Create("/app/saml/cert.key")
	if err != nil {
		return nil, err
	}
	defer keyout.Close()
	if _, err = keyout.Write(key); err != nil {
		return nil, err
	}

	return &cert, err
}

// Implémentation de SAML
func setupSaml(srv *sw.SwServer) error {

	var (
		keyPair         *tls.Certificate
		idpurl, rootUrl *url.URL
		middleware      *samlsp.Middleware
		err             error
	)

	// TODO generate an X509 certificate that is self signed.
	// Parse idp providers
	var saml_idps = SAML_IDP
	if saml_idps == "" {
		return fmt.Errorf(`saml: no $SW_SAML_IDP variable, not enabling SAML`)
	}

	// Ça c'était avec un certificat fournit, mais on va pas s'embêter et en générer un à la volée.
	// D'abord, on récupère notre certificat qui servira à parler avec l'IDP SAML contre lequel on veut s'identifier.
	// start by trying to get the certificates

	// On commence par essayer de loader les certificats dans /saml
	if fsKeyPair, err := tls.LoadX509KeyPair("/app/saml/cert.pem", "/app/saml/cert.key"); err == nil {
		keyPair = &fsKeyPair
		srv.LogInfo("saml: reusing previously generated certificate")
		// return fmt.Errorf(`when reading certificates '/cert/service.pem': %w`, err)
	}

	if keyPair == nil {
		// Si on a pas pu les loader, on les génère
		if keyPair, err = generateSelfSignedCertificate(srv.Hostnames); err != nil {
			return fmt.Errorf(`saml: unable to generate certificate: %w`, err)
		}

		srv.LogInfo(`saml: generated new certificates`)
	}

	// Ensuite on les parse, apparemment.
	if keyPair.Leaf, err = x509.ParseCertificate(keyPair.Certificate[0]); err != nil {
		return fmt.Errorf(`when parsing certificates: %w`, err)
	}

	// Il va probablement falloir qu'on puisse gérer plusieurs idp.
	var idp_array = strings.Split(saml_idps, ";")

	for _, idp_str := range idp_array {
		var split2 = strings.SplitN(idp_str, ":", 2)
		var hostnameMap = make(map[string]samlServe, 0)

		if len(split2) != 2 {
			return fmt.Errorf(`saml: in '%s', no saml provider name and url were given`, idp_str)
		}

		var provider = strings.TrimSpace(split2[0])
		var force_signed = false
		if provider[0] == '%' {
			force_signed = true
			provider = provider[1:]
			srv.LogInfo(`saml: provider '%s' will send signed requests`, provider)
		}
		var ul = strings.TrimSpace(split2[1])

		// On branche SAML sur chacun de nos hostnames
		for _, hostname := range srv.Hostnames {

			// On parse ensuite notre propre url root
			var rootstr = "https://" + hostname + "/saml/" + provider + "/"
			if rootUrl, err = url.Parse(rootstr); err != nil {
				return fmt.Errorf(`saml: could not parse root url: %w`, err)
			}

			// On récupère l'URL de notre IDP
			if idpurl, err = url.Parse(ul); err != nil {
				return fmt.Errorf(`idp url is invalid '%s': %w`, ul, err)
			}

			// Puis, on va essayer de la récupérer
			var idpmetadata, err = samlsp.FetchMetadata(context.Background(), http.DefaultClient, *idpurl)
			if err != nil {
				sw.LogError("IDP URL '", ul, "' did not resolve metadata: ", err.Error())
				err = nil
				idpmetadata = nil
			}

			if middleware, err = samlsp.New(samlsp.Options{
				URL:               *rootUrl,
				Key:               keyPair.PrivateKey.(*rsa.PrivateKey),
				Certificate:       keyPair.Leaf,
				ForceAuthn:        false,
				SignRequest:       force_signed,
				AllowIDPInitiated: true, // For dev, will remove FIXME
				IDPMetadata:       idpmetadata,
			}); err != nil {
				return fmt.Errorf(`saml: could not create samlsp provider for '%s':'%s' - %w`, provider, ul, err)
			}

			middleware.OnError = func(w http.ResponseWriter, r *http.Request, err error) {
				// debug.PrintStack()
				if err2, ok := err.(*saml.InvalidResponseError); ok {

					ReplyError(w, 500, "SAML error", "SAML encountered an error: "+err2.PrivateErr.Error())
					log.Printf("[!] saml: %s", err2.PrivateErr)
					return
				}
				ReplyError(w, 500, err.Error(), "")
			}

			// Ensuite, on parse l'url de l'IDP (identity provider)
			// var ul = "https://samltest.id/saml/idp"

			srv.LogInfo(fmt.Sprintf(`saml: SP metadata for '%s' is on '%s' for '%s'`, provider, middleware.ServiceProvider.MetadataURL.String(), hostname))

			authEndpoints = append(authEndpoints, AuthEndpoint{
				Name:        "saml:" + provider,
				DisplayName: provider,
				Hostname:    hostname,
				Endpoint:    rootstr + "saml/login",
			})

			var do_login = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// FIXME should have a way of saying which returned variable contains our user.
				username := ""

				if username == "" {
					username = samlsp.AttributeFromContext(r.Context(), "username")
				}

				if username == "" {
					username = samlsp.AttributeFromContext(r.Context(), "user")
				}

				if username == "" {
					session := samlsp.SessionFromContext(r.Context())
					if jwt_session, ok := session.(samlsp.JWTSessionClaims); ok {
						username = jwt_session.Subject
					}
				}

				if username == "" {
					ReplyError(w, 401, "No user in response", "Impossible to figure out who is trying to log in")
					ss := samlsp.SessionFromContext(r.Context())
					if ss != nil {
						ssc := ss.(samlsp.SessionWithAttributes)
						pp.Print(ssc.GetAttributes())
						// pp.Fprint(w, ssc.GetAttributes())
					}
					return
				}

				log.Printf("received saml login attempt on '%s' for '%s'", provider, username)
				doExternalLogin(srv, w, r, username)
			})

			//
			var login_serve = middleware.RequireAccount(do_login)

			hostnameMap[hostname] = samlServe{
				login:       login_serve,
				middleware:  middleware,
				hasmetadata: idpmetadata != nil,
			}
		}

		srv.Router.Route("/saml/"+provider+"/saml", func(r chi.Router) {

			r.Handle("/login", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var host = r.Host
				if item, ok := hostnameMap[host]; ok {
					if item.hasmetadata {
						item.login.ServeHTTP(w, r)
					} else {
						ReplyError(w, 503, "No SAML IDP", "The SAML provider '"+provider+"' is not configured or its metadata is unknown")
						return
					}
				} else {
					http.NotFoundHandler().ServeHTTP(w, r)
				}
			}))

			r.Mount("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var host = r.Host
				if item, ok := hostnameMap[host]; ok {
					item.middleware.ServeHTTP(w, r)
				} else {
					http.NotFoundHandler().ServeHTTP(w, r)
				}
			}))
		})
	}

	return nil
}
