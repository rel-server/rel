package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/go-chi/chi"
	"github.com/markbates/goth"
	"github.com/markbates/goth/gothic"
	"github.com/markbates/goth/providers/google"
	"github.com/markbates/goth/providers/salesforce"
	"sales-way.com/server/oauth/yahoo"
	"sales-way.com/server/sw"
)

func SetupOAuth(srv *sw.SwServer) error {

	var register = func(provider, uid, secret, hostname string, prov func(uid, secret, cbk string) (goth.Provider, error)) {
		var (
			uid_val    = os.Getenv(uid)
			secret_val = os.Getenv(secret)
		)

		if stringsHaveValue(uid_val, secret_val) {
			var login = fmt.Sprintf("https://%s/oauth/%s/login", hostname, provider)
			var cbk = fmt.Sprintf("https://%s/oauth/%s/callback", hostname, provider)
			provimpl, err := prov(uid_val, secret_val, cbk)
			if err != nil {
				srv.LogError(`oauth: provider `, provider, ` errored: `, err)
				return
			}

			provimpl.SetName(provider + ":" + hostname)

			srv.LogInfo(`oauth: provider `, provimpl.Name(), ` on `, cbk, " for ", hostname)

			goth.UseProviders(provimpl)

			authEndpoints = append(authEndpoints, AuthEndpoint{
				Name:        "oauth:" + provider,
				DisplayName: provider,
				Endpoint:    login,
				Hostname:    hostname,
			})
		} else {
			// srv.LogWarn(`oauth: provider `, provider, ` is disabled, use `, uid, ` and `, secret, ` variables`)
		}
	}

	for _, hostname := range srv.Hostnames {
		register("google", "SW_GOOGLE_KEY", "SW_GOOGLE_SECRET", hostname, func(uid, secret, cbk string) (goth.Provider, error) {
			return google.New(uid, secret, cbk), nil
		})
		register("salesforce", "SW_SFDC_KEY", "SW_SFDC_SECRET", hostname, func(uid, secret, cbk string) (goth.Provider, error) {
			return salesforce.New(uid, secret, cbk), nil
		})
		register("yahoo", "SW_YAHOO_KEY", "SW_YAHOO_SECRET", hostname, func(uid, secret, cbk string) (goth.Provider, error) {
			return yahoo.New(uid, secret, cbk, "email"), nil

		})
	}

	// https://api.orange.com/oauth/v3/.well-known/oauth-authorization-server

	// AuthEndpoint     string `json:"authorization_endpoint"`
	// TokenEndpoint    string `json:"token_endpoint"`
	// UserInfoEndpoint string `json:"userinfo_endpoint"`
	// Issuer           string `json:"issuer"`

	// time.AfterFunc(3*time.Second, func() {
	// 	register("orange", "SW_ORANGE_KEY", "SW_ORANGE_SECRET", func(uid, secret, cbk string) (goth.Provider, error) {
	// 		res, err := openidConnect.New(uid, secret, "https://"+srv.Hostname+"/oauth/orange/callback", "http://127.0.0.1:3001/oauth/orange/.well-known/openid-configuration", "openid", "form_filling")
	// 		if err != nil {
	// 			log.Print("oauth: ", err)
	// 			return nil, err
	// 		}
	// 		res.SetName("orange")
	// 		return res, nil
	// 	})
	// })

	///////////////////////////////////// END OF CONFIGURATION ////////////////////////////////////////

	// This is how we supply the provider to goth, even when using another router than gorilla/mux
	gothic.GetProviderName = func(r *http.Request) (string, error) {
		var res = chi.URLParam(r, "provider")
		if res == "" {
			return "", fmt.Errorf(`urlparam '%s' was empty`, "provider")
		}
		return res + ":" + r.Host, nil
	}

	// Pour que Orange fonctionne, il faut créer une app chez eux avec les API
	// - Authentication / Authorization
	// - Form ID France (pour les user infos)
	srv.Router.Get("/oauth/orange/.well-known/openid-configuration", func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(200)
		rw.Write([]byte(`{
		"authorization_endpoint": "https://api.orange.com/openidconnect/fr/v1/authorize",
		"token_endpoint": "https://api.orange.com/openidconnect/fr/v1/token",
		"userinfo_endpoint": "https://api.orange.com/formfilling/fr/v1/userinfo",
		"issuer": "https://openid.orange.fr"
		}`))
	})

	srv.Router.Get("/oauth/{provider}/login", gothic.BeginAuthHandler)

	srv.Router.Get("/oauth/{provider}/callback", func(rw http.ResponseWriter, req *http.Request) {

		// this returns user
		user, err := gothic.CompleteUserAuth(rw, req)
		if err != nil {
			fmt.Fprintln(rw, err)
			return
		}

		srv.LogInfo("received oauth user ", user.UserID, ", ", user.Email, " from ", chi.URLParamFromCtx(req.Context(), "provider"))
		// FIXME login in pgx and try to see if it works
		// Also, set accesstoken from the resulting token sent by the database
		if srv.HasPg() {
			doExternalLogin(srv, rw, req, user.Email)
		}
	})

	return nil
}
