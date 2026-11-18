# CBOR is always enabled, with no configuration flag

CBOR is a second wire format beside JSON, chosen per request by `Content-Type` (the body) and `Accept` (the response). Since a client only gets CBOR by asking for it, enabling it cannot change what an existing JSON client sends or receives, so there is no `http.cbor.enabled` flag to set or forget. JSON stays the default everywhere.
