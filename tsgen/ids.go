package tsgen

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/rel-server/rel/pg"
)

// idResolver decides which columns get a table-local id type (pg_values.ts's `Id` interface,
// docs/content/typescript/index.md ## Typed wire values ### Table-local id types) : a column that Postgres
// fills in from a sequence (serial/bigserial/smallserial, or identity), plus every foreign-key column that
// points at one, directly or through a chain of foreign keys.
type idResolver struct {
	// key is the `Id` interface key a column's type is looked up under : "schema.table.column" of the root
	// serial/identity column. Only holds columns that actually get an id type.
	key map[*pg.Column]string

	// roots lists each serial/identity column of a target relation once, sorted by key, for the `Id` declaration.
	roots []idRoot
}

type idRoot struct {
	key  string
	base string // "number" or "Int8", the wire representation every column typed with this root shares
}

// isOwnIdColumn is true for a column Postgres fills in from a sequence : identity, or a serial default
// (`nextval('...')`, which is also how info_relation.go reports an identity column's default).
func isOwnIdColumn(c *pg.Column) bool {
	return c.IsIdentity || strings.HasPrefix(c.DefaultExpression, "nextval(")
}

// idBase is the TS type an id column is branded onto, or "" when the column isn't an integer id candidate
// (a domain over an integer resolves to its Type__ name here, and so is skipped).
func idBase(c *pg.Column, tc *typeCollector) string {
	switch b := tsTypeExpr(c.Type, tc); b {
	case "number", "Int8":
		return b
	}
	return ""
}

// newIdResolver walks every target relation. A foreign key column is paired position by position with the
// column it references (so a composite key maps column by column), and the chain is followed to its root. A
// column that is itself serial/identity keeps its own type even when it also references another one. A column
// whose wire representation differs from its root's (an int4 referencing a bigserial) is left alone, since the
// brand can't describe both a number and a string.
func newIdResolver(db *pg.DbInfos, relations []*pg.Relation, tc *typeCollector) *idResolver {
	allowed := map[*pg.Relation]bool{}
	for _, r := range relations {
		allowed[r] = true
	}

	// column -> the column it references, from every relation (a chain may pass through one that isn't a target).
	references := map[*pg.Column]*pg.Column{}
	owner := map[*pg.Column]*pg.Relation{}
	for _, r := range db.Relations {
		for _, c := range r.Columns {
			owner[c] = r
		}
		for _, fk := range r.OutgoingForeignKeys {
			if fk.Target == nil {
				continue
			}
			for i, c := range fk.Columns {
				if i < len(fk.Target.Columns) && c != nil && fk.Target.Columns[i] != nil {
					references[c] = fk.Target.Columns[i]
				}
			}
		}
	}

	res := &idResolver{key: map[*pg.Column]string{}}
	rootKey := func(c *pg.Column) string {
		id := owner[c].Identifier
		return id.Schema + "." + id.Name + "." + c.Name
	}

	// root follows references until it reaches a serial/identity column of a target relation.
	root := func(c *pg.Column) *pg.Column {
		seen := map[*pg.Column]bool{}
		for c != nil && !seen[c] {
			seen[c] = true
			if isOwnIdColumn(c) {
				if allowed[owner[c]] {
					return c
				}
				return nil
			}
			c = references[c]
		}
		return nil
	}

	roots := map[string]string{}
	for _, r := range relations {
		for _, c := range r.Columns {
			rc := root(c)
			if rc == nil {
				continue
			}
			base := idBase(rc, tc)
			if base == "" || idBase(c, tc) != base {
				continue
			}
			k := rootKey(rc)
			res.key[c] = k
			roots[k] = base
		}
	}
	for k, base := range roots {
		res.roots = append(res.roots, idRoot{key: k, base: base})
	}
	sort.Slice(res.roots, func(i, j int) bool { return res.roots[i].key < res.roots[j].key })
	return res
}

// typeExpr is the `Id[...]` reference for c, or "" when c has no id type.
func (r *idResolver) typeExpr(c *pg.Column) string {
	if r == nil {
		return ""
	}
	if k, ok := r.key[c]; ok {
		return "Id[" + strconv.Quote(k) + "]"
	}
	return ""
}

// declaration renders the generated half of the `Id` interface (merged with pg_values.ts's own empty one) ; empty
// when no column has an id type.
func (r *idResolver) declaration() string {
	if len(r.roots) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("export interface Id {\n")
	for _, root := range r.roots {
		fmt.Fprintf(&b, "  %s: %s & { readonly __id: %s }\n", strconv.Quote(root.key), root.base, strconv.Quote(root.key))
	}
	b.WriteString("}\n\n")
	return b.String()
}
