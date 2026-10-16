// Points every query at the test server (test/tsclient starts it and passes its address in REL_URL).
import { Querier } from "./generated/database"

const url = process.env.REL_URL
if (!url) {
  throw new Error("REL_URL is not set : run these tests through `go test ./test/tsclient`")
}
Querier.prototype.baseUrl = url
