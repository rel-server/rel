package config

// Test returns a hand-written Config for tests, in a non-test file so other packages can import it. Host and Port
// are left zero for the caller to fill from the test container. User is the container's superuser, which a real
// deployment's login must never be.
func Test() *Config {
	return &Config{
		Pg: Pg{
			User:         "postgres",
			Password:     "postgres",
			NoticesLevel: "off",
			Query: PgQuery{
				MaxDepth: DefaultMaxDepth,
			},
		},
		Logging: Logging{
			Handler: DefaultLoggingHandler,
			Level:   DefaultLoggingLevel,
		},
		Http: Http{
			CookiesMaxAge:  DefaultHttpCookiesMaxAge,
			MaxBodySize:    DefaultHttpMaxBodySize,
			MaxPartCount:   DefaultHttpMaxPartCount,
			ResponseBuffer: DefaultHttpResponseBuffer,
			Upload:         HttpUpload{MaxSize: DefaultHttpMaxBodySize},
			Static:         HttpStatic{Path: DefaultHttpStaticPath},
			Templates:      HttpTemplates{Path: DefaultHttpTemplatesPath},
			Cors: HttpCors{
				AllowedMethods: DefaultHttpCorsAllowedMethods,
				AllowedHeaders: DefaultHttpCorsAllowedHeaders,
				MaxAge:         DefaultHttpCorsMaxAge,
			},
			Csp: HttpCsp{
				DefaultSrc: DefaultHttpCspDefaultSrc,
			},
		},
		// A literal : Test() is not loaded, so $FILE$ is never resolved.
		Jwt: Jwt{
			Secret:        "test-jwt-secret-not-for-production-use",
			CookieName:    DefaultJwtCookieName,
			Algorithm:     DefaultJwtAlgorithm,
			SameSite:      DefaultJwtSameSite,
			MaxAge:        DefaultJwtMaxAge,
			RenewAfter:    DefaultJwtRenewAfter,
			MaxSessionAge: DefaultJwtMaxSessionAge,
		},
		Reload: Reload{
			Timeout:      DefaultReloadTimeout,
			DrainTimeout: DefaultReloadDrainTimeout,
		},
		Dmut: Dmut{
			Timeout: DefaultDmutTimeout,
			Mode:    DmutModeDefault,
		},
		Blacklist: DefaultBlacklist(),
	}
}
