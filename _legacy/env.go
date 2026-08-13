package main

import (
	"os"
	"slices"
)

// getenvOrDefault gets the first non empty or non nil variable, where the last
// given string is in fact the default value.
// It also checks for secrets potentially given to a docker container in
// /var/run/secrets/<var>.txt
func getenvOrDefault(vars ...string) string {
	var l = len(vars)
	for i, v := range vars {
		if i == l-1 {
			return v
		}
		var get = os.Getenv(v)
		if get != "" {
			return get
		}
	}
	return ""
}

// stringsHaveValue returns true if all strings provided
// are different from ""
func stringsHaveValue(vars ...string) bool {
	return !slices.Contains(vars, "")
}

var (
	DISABLE_PASSWD = getenvOrDefault("SW_DISABLE_PASSWD", "")

	JWT_ACCESSTOKEN_EXPIRY = getenvOrDefault("SW_JWT_ACCESSTOKEN_EXPIRY", "")
	JWT_SECRET             = getenvOrDefault("SW_JWT_SECRET", "PGRST_JWT_SECRET", "")
	JWT_COOKIE             = getenvOrDefault("SW_JWT_COOKIE", "PGRST_JWT_COOKIE", "accesstoken")

	DB_ANON_ROLE         = getenvOrDefault("SW_DB_ANON_ROLE", "PGRST_DB_ANON_ROLE", "")
	DB_IMPERSONATOR_ROLE = getenvOrDefault("SW_IMPERSONATOR_ROLE", "")

	SAML_IDP = getenvOrDefault("SW_SAML_IDP", "")

	DEBUG_ENABLED     = getenvOrDefault("SW_ENABLE_DEBUG", "") == "true"
	ENABLE_TS_SCHEMAS = getenvOrDefault("SW_ENABLE_TS_SCHEMAS", "")
)
