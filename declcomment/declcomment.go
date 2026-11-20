// Package declcomment reads the declarations a COMMENT ON can carry : a comment starting `<kind>::` is one inline
// HUML document, one starting `<kind>:` then `{` a JSON object. Any other comment is plain doc text.
package declcomment

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/huml-lang/go-huml"
	"github.com/samber/oops"
)

// Extract returns, as JSON, the declaration of kind comment holds ; found is false for a plain comment, and err
// is set when the comment is prefixed but does not parse.
func Extract(comment, kind string) (raw []byte, found bool, err error) {
	trimmed := strings.TrimSpace(comment)
	rest, ok := strings.CutPrefix(trimmed, kind)
	if !ok {
		return nil, false, nil
	}
	switch {
	case strings.HasPrefix(rest, "::"):
		var doc map[string]any
		if err := huml.Unmarshal([]byte(trimmed), &doc); err != nil {
			return nil, true, oops.Wrapf(err, "%s: parsing HUML declaration", kind)
		}
		v, ok := doc[kind]
		if !ok {
			return nil, true, oops.Errorf("%s: HUML declaration has no %q key", kind, kind)
		}
		if raw, err = json.Marshal(v); err != nil {
			return nil, true, oops.Wrapf(err, "%s: re-encoding HUML declaration", kind)
		}
		return raw, true, nil
	case isJSONObjectAfterColon(rest):
		return []byte(strings.TrimSpace(rest[1:])), true, nil
	}
	return nil, false, nil
}

// isJSONObjectAfterColon reports ":", optional whitespace, then "{", so a doc comment beginning "route: see below"
// isn't mistaken for a declaration.
func isJSONObjectAfterColon(rest string) bool {
	rest, ok := strings.CutPrefix(rest, ":")
	return ok && strings.HasPrefix(strings.TrimSpace(rest), "{")
}

// TypeDecl is a declared TypeScript type : `column::`, `type::`, a well-known param's `ts`, a typescript.types
// entry. A zero Type leaves the generated type as it is.
type TypeDecl struct {
	// Comment is the `/** */` text.
	Comment string `json:"comment"`
	// Type is a TypeScript type expression, emitted as written ; with From, a name that module exports.
	Type string `json:"type"`
	// From is the module Type is imported from, as an import statement writes it.
	From string `json:"from"`
}

// Decode reads a TypeDecl from a JSON object, refusing a field it does not know.
func Decode(raw []byte) (TypeDecl, error) {
	var d TypeDecl
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return TypeDecl{}, oops.Wrapf(err, "decoding the declaration")
	}
	return d, d.Validate()
}

// Validate refuses what would break the generated file : a line break in Type or From, a quote in From, a From
// without a Type, and with From a Type that is not a bare name.
func (d TypeDecl) Validate() error {
	if strings.ContainsAny(d.Type, "\r\n") {
		return oops.Errorf("type %q holds a line break", d.Type)
	}
	if d.From == "" {
		return nil
	}
	if strings.ContainsAny(d.From, "\r\n\"'`\\") {
		return oops.Errorf("from %q holds a line break, a quote or a backslash", d.From)
	}
	if !IsIdentifier(d.Type) {
		return oops.Errorf("type %q is not a name, as an import from %q needs", d.Type, d.From)
	}
	return nil
}

// IsIdentifier reports a plain TypeScript identifier (ASCII only).
func IsIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		letter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || r == '$'
		if !letter && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}
