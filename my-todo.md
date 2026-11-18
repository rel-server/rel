
- getting started : github release or docker (docker is best), sources come with the test/demo fixture
- `/rel/database.ts` and `/rel/database.json` are regenerated and sent in full on every request : add an ETag (schema version + request options) and answer 304 Not Modified
- a cross-site top-level `GET /rel` carries the JWT cookie under `SameSite=Lax` ; a third party can't read the answer, but someone watching the network sees its size, and a filter on a column turns that size into a yes/no on the column's value. Decide whether `GET /rel` should require something a cross-site navigation can't send
- `/rel` errors come in two formats : the handler's own are JSON, but `route.GateMiddleware` (auth failures, transaction errors) and the `Content-Encoding` 415 in `boot/mux.go` answer plain text on `/rel` too. Decide whether `/rel` should always answer JSON
