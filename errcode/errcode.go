// Package errcode holds the `code` tokens of error responses that rel itself picks ; RSxxx and PG_* codes come
// from pgerr. A leaf package, so any package can import it.
package errcode

// Code is a stable, machine-readable error identifier, present on every error response.
type Code string

// Header is the response header every error response carries its Code in.
const Header = "X-Rel-Errorcode"

// Fallbacks for an error with no more specific code.
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

// AnonymousDisabledMessage is the message every AnonymousDisabled error uses.
const AnonymousDisabledMessage = "anonymous access is disabled"

// Query compile errors.
const (
	QueryMalformedJSON         Code = "QUERY_MALFORMED_JSON"
	QueryMalformedCBOR         Code = "QUERY_MALFORMED_CBOR"
	QueryInvalidExpression     Code = "QUERY_INVALID_EXPRESSION"
	UnknownIdentifier          Code = "UNKNOWN_IDENTIFIER"
	JoinMissingIndex           Code = "JOIN_MISSING_INDEX"
	WriteForbidden             Code = "WRITE_FORBIDDEN"
	WriteForbiddenFunctionRoot Code = "WRITE_FORBIDDEN_FUNCTION_ROOT"
)

// ComplexQuery errors.
const (
	QueryInvalidReturns         Code = "QUERY_INVALID_RETURNS"
	QueryUnsupportedReturns     Code = "QUERY_UNSUPPORTED_RETURNS"
	QueryCountIsReadOnly        Code = "QUERY_COUNT_IS_READ_ONLY"
	QueryStatsIsWriteOnly       Code = "QUERY_STATS_IS_WRITE_ONLY"
	QueryStatsQueryPlanConflict Code = "QUERY_STATS_QUERY_PLAN_CONFLICT"
	QueryRollbackNotGranted     Code = "QUERY_ROLLBACK_NOT_GRANTED"
	QueryComplexNotAllowedOnGet Code = "QUERY_COMPLEX_NOT_ALLOWED_ON_GET"
)

// rel() in templates, which is read-only.
const (
	RelFuncWriteNotAllowed Code = "REL_FUNC_WRITE_NOT_ALLOWED"
)

// Well-known query compile errors, raised at load/reload time.
const (
	WellKnownDuplicateName Code = "WELL_KNOWN_DUPLICATE_NAME"
	WellKnownUnusedParam   Code = "WELL_KNOWN_UNUSED_PARAM"
	WellKnownUnknownParam  Code = "WELL_KNOWN_UNKNOWN_PARAM"
)

// Well-known query execution errors, raised per request.
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

// SSO protocol failures rel detects ; the callback function's errors use RSxxx.
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
