// Package errcode is the shared `code` token space specs/error-handling.md
// ## Error codes describes : a leaf package (no dependencies within this
// module) so both server and route — and pgerr, for its PG_* family — can
// depend on it without risking an import cycle.
//
// Two of the three code families documented in error-handling.md live
// elsewhere : Postgres-raised RSxxx codes and the PG_* classification table
// are pgerr's own concern (pgerr.Classify), since they're derived from a
// *pgconn.PgError, not picked at a Go call site. Only the rel-internal,
// SCREAMING_SNAKE_CASE family — attached via `oc.Code(errcode.XXX)` at the
// point rel itself rejects a request — lives here.
package errcode

// Code is a stable, machine-readable error identifier — see
// specs/error-handling.md ## Error codes. Always present on an error
// response, regardless of status ; there is no absent/unclassified case,
// only the Internal/Unclassified fallbacks below.
type Code string

// Header is the response header every error response carries `Code` in,
// regardless of body framing — docs/content/configuration/operations.md
// ## Error responses.
const Header = "X-Rel-Errorcode"

// Fallbacks for an error reaching a response with no more specific code
// assigned — ## Error codes : "there is no unclassified/silent case."
const (
	Internal     Code = "INTERNAL"     // 5xx, no more specific code applies
	Unclassified Code = "UNCLASSIFIED" // 4xx, no more specific code applies
)

// Transport — never reaches application logic.
const (
	MethodNotAllowed     Code = "METHOD_NOT_ALLOWED"
	RouteNotFound        Code = "ROUTE_NOT_FOUND"
	MalformedBody        Code = "MALFORMED_BODY"
	UnsupportedMediaType Code = "UNSUPPORTED_MEDIA_TYPE"
	BodyTooLarge         Code = "BODY_TOO_LARGE"
	MalformedMultipart   Code = "MALFORMED_MULTIPART"
)

// Auth / authorization.
const (
	AnonymousDisabled       Code = "ANONYMOUS_DISABLED"
	AnonymousRouteForbidden Code = "ANONYMOUS_ROUTE_FORBIDDEN"
	NoRoleConfigured        Code = "NO_ROLE_CONFIGURED"
)

// AnonymousDisabledMessage is specs/authentication.md "# Roles ## Anonymous
// role existence"'s own wording — the message text at every one of this
// code's call sites (server/rel.go, route/handler.go, static/static.go),
// shared here so the three can't independently drift on it.
const AnonymousDisabledMessage = "anonymous access is disabled"

// Query compile errors (query-engine.md).
const (
	QueryMalformedJSON         Code = "QUERY_MALFORMED_JSON"
	QueryInvalidExpression     Code = "QUERY_INVALID_EXPRESSION"
	UnknownIdentifier          Code = "UNKNOWN_IDENTIFIER"
	JoinMissingIndex           Code = "JOIN_MISSING_INDEX"
	WriteForbidden             Code = "WRITE_FORBIDDEN"
	WriteForbiddenFunctionRoot Code = "WRITE_FORBIDDEN_FUNCTION_ROOT"
)

// Well-known query compile errors (## Compilation Errors) — raised once,
// at load/reload time, never re-checked per request.
const (
	WellKnownDuplicateName Code = "WELL_KNOWN_DUPLICATE_NAME"
	WellKnownUnusedParam   Code = "WELL_KNOWN_UNUSED_PARAM"
	WellKnownUnknownParam  Code = "WELL_KNOWN_UNKNOWN_PARAM"
)

// Well-known query execution errors (specs/well-known-queries.md ##
// Execution Errors) — raised per request, against a /wellknown invocation.
const (
	WellKnownUnknownQuery      Code = "WELL_KNOWN_UNKNOWN_QUERY"
	WellKnownParamTypeMismatch Code = "WELL_KNOWN_PARAM_TYPE_MISMATCH"
	WellKnownParamRequired     Code = "WELL_KNOWN_PARAM_REQUIRED"
)

// Runtime / infra.
const (
	DBUnavailable    Code = "DB_UNAVAILABLE"
	TransactionError Code = "TRANSACTION_ERROR"
	TemplateError    Code = "TEMPLATE_ERROR"
	UploadIOError    Code = "UPLOAD_IO_ERROR"
)

// Upload-specific.
const (
	UploadConflict  Code = "UPLOAD_CONFLICT"
	UploadTooLarge  Code = "UPLOAD_TOO_LARGE"
	UploadWrongType Code = "UPLOAD_WRONG_TYPE"
)

// SSO (specs/oauth-saml.md) — protocol-level failures Go itself detects,
// distinct from the callback function's own RSxxx convention
// (## Callback function), which never reaches this package.
const (
	SsoNotReady            Code = "SSO_NOT_READY"             // 503 : discovery/IdP metadata hasn't resolved yet
	SsoInternal            Code = "SSO_INTERNAL"              // 500 : rel's own logic failed (state/nonce generation, encoding)
	SsoBadRequest          Code = "SSO_BAD_REQUEST"           // 400 : malformed callback request (missing code, unparseable form)
	SsoBadState            Code = "SSO_BAD_STATE"             // 400 : missing/mismatched OAuth2 state
	SsoBadNonce            Code = "SSO_BAD_NONCE"             // 400 : ID token nonce doesn't match the one this login minted
	SsoTokenExchangeFailed Code = "SSO_TOKEN_EXCHANGE_FAILED" // 502 : the issuer's token endpoint rejected the exchange
	SsoNoIdToken           Code = "SSO_NO_ID_TOKEN"           // 502 : token response carried no id_token
	SsoInvalidIdToken      Code = "SSO_INVALID_ID_TOKEN"      // 502 : id_token failed signature/issuer/audience verification
	SsoUserinfoFailed      Code = "SSO_USERINFO_FAILED"       // 502 : fetch_userinfo's own call to the issuer failed
	SsoSamlInvalidResponse Code = "SSO_SAML_INVALID_RESPONSE" // 400 : SAML response/assertion failed to parse or verify
)
