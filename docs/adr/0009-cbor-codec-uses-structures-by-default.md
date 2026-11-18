# The TypeScript `cbor` codec uses structures by default, both ways

A response's shapes come from the query and are never degenerate, so the codec asks for structures in responses. It also sends them in request bodies, with `sendStructures` to turn that off for a body whose objects rarely share their keys : a dictionary-like `jsonb` value costs about 2.4 times the encoding time of maps, for no size gain. Never `sequential`, which would refer to structures defined by an earlier body, while the server reads each request alone.
