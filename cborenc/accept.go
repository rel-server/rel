package cborenc

import (
	"mime"
	"strconv"
	"strings"
)

// MediaType is the media type of a CBOR response.
const MediaType = "application/cbor"

// Accepts reports whether an Accept header asks for CBOR rather than JSON. JSON is the default : an absent or
// empty header, "*/*", "application/*" and a tie between the two named types all answer JSON. CBOR is answered when
// it is listed with a higher quality than JSON gets, when JSON is not acceptable at all, or when it is named and
// JSON only matches a wildcard at the same quality.
func Accepts(accept string) bool {
	if strings.TrimSpace(accept) == "" {
		return false
	}
	// the quality each range gives a type : the most specific range that matches wins
	cbor, json := -1.0, -1.0
	spec := func(best *float64, bestSpec *int, specificity int, q float64) {
		if specificity > *bestSpec {
			*best, *bestSpec = q, specificity
		}
	}
	cborSpec, jsonSpec := 0, 0
	for _, part := range strings.Split(accept, ",") {
		mt, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		q := 1.0
		if v, ok := params["q"]; ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				q = f
			}
		}
		switch mt {
		case "application/cbor":
			spec(&cbor, &cborSpec, 3, q)
		case "application/json":
			spec(&json, &jsonSpec, 3, q)
		case "application/*":
			spec(&cbor, &cborSpec, 2, q)
			spec(&json, &jsonSpec, 2, q)
		case "*/*":
			spec(&cbor, &cborSpec, 1, q)
			spec(&json, &jsonSpec, 1, q)
		}
	}
	if cbor <= 0 {
		return false
	}
	if cbor == json {
		// equal preference : a type the client named beats one it only matched with a wildcard, and JSON wins otherwise
		return cborSpec > jsonSpec
	}
	return cbor > json
}
