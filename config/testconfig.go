package config

// Test returns a hand-written Config for tests : the spec'd defaults
// (DefaultBlacklist, DefaultMaxDepth) plus credentials matching this repo's
// testcontainers convention (pg/info_test.go's postgres:16-alpine container,
// and testcontainers-go's own default superuser). Exported from a non-_test
// file, deliberately, so packages other than config itself (query execution,
// once that exists) can import it directly rather than duplicating it.
//
// Host/Port are left at their zero value : testcontainers assigns an
// ephemeral port per container run, so there's no fixed value to hand-write
// here — callers fill those in from the actual container connection info.
// Pg.User/Password/AnonymousRole are left unset for the same reason this
// whole file exists : nothing exercises them yet, so there's nothing to
// hand-write that wouldn't just be a guess.
//
// Not meant to represent a real deployment's config : Query.User here is
// the container's superuser, which query-engine.md ## Scoping explicitly says a
// real pg.query.user must never be. Fine for exercising query building/
// running against a disposable test database ; not something to reach for
// once the role-restriction check from that section actually exists.
func Test() *Config {
	return &Config{
		Pg: Pg{
			Query: PgQuery{
				Login: Login{
					User:     "postgres",
					Password: "postgres",
				},
				MaxDepth: DefaultMaxDepth,
			},
		},
		Logging: Logging{
			Handler: DefaultLoggingHandler,
			Level:   DefaultLoggingLevel,
		},
		Http: Http{
			RequestDomainName:  DefaultHttpRequestDomainName,
			ResponseDomainName: DefaultHttpResponseDomainName,
			UploadDomainName:   DefaultHttpUploadDomainName,
			CookiesMaxAge:      DefaultHttpCookiesMaxAge,
			MaxBodySize:        DefaultHttpMaxBodySize,
			MaxPartCount:       DefaultHttpMaxPartCount,
			Static:             HttpStatic{Path: DefaultHttpStaticPath},
			Templates:          HttpTemplates{Path: DefaultHttpTemplatesPath},
			Cors: HttpCors{
				AllowedMethods: DefaultHttpCorsAllowedMethods,
				AllowedHeaders: DefaultHttpCorsAllowedHeaders,
				MaxAge:         DefaultHttpCorsMaxAge,
			},
			Csp: HttpCsp{
				DefaultSrc: DefaultHttpCspDefaultSrc,
			},
		},
		// A fixed literal, not DefaultJwtSecret's "$FILE$..." form : Test()
		// bypasses loader.go's $FILE$ resolution entirely (hand-built, not loaded).
		Jwt: Jwt{
			Secret:        "test-jwt-secret-not-for-production-use",
			CookieName:    DefaultJwtCookieName,
			Algorithm:     DefaultJwtAlgorithm,
			SameSite:      DefaultJwtSameSite,
			MaxAge:        DefaultJwtMaxAge,
			RenewAfter:    DefaultJwtRenewAfter,
			MaxSessionAge: DefaultJwtMaxSessionAge,
		},
		Dmut: Dmut{
			Path:               DefaultDmutPath,
			ReloadDrainTimeout: DefaultDmutReloadDrainTimeout,
		},
		Blacklist: DefaultBlacklist(),
	}
}
