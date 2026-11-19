# Request bodies may be compressed, responses are left to the proxy

Browsers never compress a request body on their own and proxies do not decompress one, so rel decompresses `gzip`, `deflate` and `zstd` request bodies itself (`klauspost/compress` for zstd). Brotli is left out : browsers' `CompressionStream` cannot produce it, and a non-browser client can send zstd. Responses stay uncompressed, compressed by the reverse proxy in front of rel.

## Consequences

- `http.max_body_size` bounds the decompressed bytes, and reading stops as soon as it is exceeded.
- More than one coding in `Content-Encoding` is refused, as is any other coding : a 415 with `Accept-Encoding: gzip, deflate, zstd, identity`.
- The TypeScript codec gzips request bodies above a size threshold. Gzip is enough there, and it replaces records as the way to shrink a body.
