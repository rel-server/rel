package tsgen

import (
	"fmt"
	"strings"

	"github.com/samber/oops"

	"github.com/rel-server/rel/config"
)

// Overrides are what one request (or one command-line flag) may change of the configured typescript.*
// options. A blank field is no override.
type Overrides struct {
	// Schemas is the `schemas` query param : a comma-separated list that narrows typescript.schemas, never widens it.
	Schemas string
	// Codec is the `codec` query param or --typescript-codec : it replaces typescript.codec, it is not merged into it.
	Codec string
}

// BadOverrideError is an Overrides value that cannot be applied : the caller's mistake (a 400), as
// opposed to a bad configured value. Its message is the explanation to give back.
type BadOverrideError struct{ Err error }

func (e *BadOverrideError) Error() string { return e.Err.Error() }
func (e *BadOverrideError) Unwrap() error { return e.Err }

// Resolve is the Options of one generated file : the configured typescript.* section and blacklist,
// with ov applied. A configured value that does not parse is an error of its own, so startup can
// check the whole section with Resolve(cfg, Overrides{}).
func Resolve(cfg *config.Config, ov Overrides) (Options, error) {
	configured := parseSchemaList(cfg.TypeScript.Schemas)
	schemas, err := narrowSchemas(configured, parseSchemaList(ov.Schemas))
	if err != nil {
		return Options{}, &BadOverrideError{Err: err}
	}

	var codec Codec
	if strings.TrimSpace(ov.Codec) != "" {
		if codec, err = ParseCodec(ov.Codec); err != nil {
			return Options{}, &BadOverrideError{Err: err}
		}
	} else if codec, err = ParseCodec(cfg.TypeScript.Codec); err != nil {
		return Options{}, oops.Wrapf(err, "typescript.codec")
	}

	return Options{Schemas: schemas, Blacklist: cfg.Blacklist, Codec: codec}, nil
}

// narrowSchemas is the `schemas` override intersected with typescript.schemas' whitelist : the
// override never widens past the whitelist, and a name outside it is an error, not a silent drop,
// so a typo surfaces instead of quietly exporting less than asked.
func narrowSchemas(configured, override []string) ([]string, error) {
	if len(configured) == 0 {
		return override, nil // no whitelist means every schema ; the override alone decides
	}
	if len(override) == 0 {
		return configured, nil
	}
	allowed := make(map[string]bool, len(configured))
	for _, s := range configured {
		allowed[s] = true
	}
	out := make([]string, 0, len(override))
	for _, s := range override {
		if !allowed[s] {
			return nil, fmt.Errorf("schemas=%q is not in the configured typescript.schemas whitelist", s)
		}
		out = append(out, s)
	}
	return out, nil
}
