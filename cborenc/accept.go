package cborenc

import (
	"mime"
	"strconv"
	"strings"
)

// MediaType is the media type of a CBOR response.
const MediaType = "application/cbor"

// MediaTypeStructures is the media type of a CBOR response that uses structures (specs/cbor.md ## Structures).
const MediaTypeStructures = MediaType + "; structures=1"

// Format is the representation a request's Accept header selects.
type Format uint8

const (
	// JSON is the default.
	JSON Format = iota
	// CBOR is CBOR with plain maps.
	CBOR
	// CBORStructures is CBOR whose records are structures.
	CBORStructures
)

// MediaType returns the Content-Type that announces f.
func (f Format) MediaType() string {
	switch f {
	case CBOR:
		return MediaType
	case CBORStructures:
		return MediaTypeStructures
	}
	return "application/json"
}

// Accepts reports whether an Accept header asks for CBOR rather than JSON, structures or not. See Negotiate.
func Accepts(accept string) bool { return Negotiate(accept) != JSON }

// Negotiate chooses the representation an Accept header asks for. JSON is the default : an absent or empty header,
// "*/*", "application/*" and a tie between the two named types all answer JSON. CBOR is answered when it is listed
// with a higher quality than JSON gets, when JSON is not acceptable at all, or when it is named and JSON only
// matches a wildcard at the same quality. Of two listings of application/cbor, the one with the highest quality
// wins, and the one with the parameter structures=1 (or true) on a tie.
func Negotiate(accept string) Format {
	if strings.TrimSpace(accept) == "" {
		return JSON
	}
	// the quality each range gives a type : the most specific range that matches wins
	cbor, json := -1.0, -1.0
	spec := func(best *float64, bestSpec *int, specificity int, q float64) {
		if specificity > *bestSpec {
			*best, *bestSpec = q, specificity
		}
	}
	cborSpec, jsonSpec := 0, 0
	plainQ, structQ := -1.0, -1.0 // the two listings of application/cbor
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
			if s := params["structures"]; s == "1" || s == "true" {
				structQ = max(structQ, q)
			} else {
				plainQ = max(plainQ, q)
			}
			cborSpec = 3
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
	if cborSpec == 3 {
		cbor = max(plainQ, structQ)
	}
	if cbor <= 0 {
		return JSON
	}
	if cbor == json {
		// equal preference : a type the client named beats one it only matched with a wildcard, and JSON wins otherwise
		if cborSpec <= jsonSpec {
			return JSON
		}
	} else if cbor < json {
		return JSON
	}
	if cborSpec == 3 && structQ >= plainQ {
		return CBORStructures
	}
	return CBOR
}
