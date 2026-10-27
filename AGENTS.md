# General instructions

- ALWAYS Create regression and integration tests using either plain golang testing or testcontainers with a sample postgres database.
- Test your claims/assertions instead of only recalling when your confidence level is not impeccable
- DRY : keep the code simple, avoid repetitions and factorize code whenever possible.
- Implementations MUST always be the most efficient CPU/RAM wise. If a compromise is to be made, prompt the user
- Always prompt the user whenever you deem an important architectural decision is to be made (adding/removing a library, implementation details/philosophy, performance concerns)
- Explain the code by adding comments, following [[AGENTS#Comment style]]'s directions
- If there is a TODO.md file somewhere, keep it updated with what's been done
- When replying or writing specs / docs / comments, use plain english over lingo and buzzwords ; stay clear and legible by non-senior developers.
- When alerting me on problems or inconsistencies, use examples if the explanation is complex

- ALWAYS Update docs as you work on the code : docs MUST always be relevant.
- NEVER rm -rf previously produced docs when testing them with zensical ; it messes up the live server
- In markdown, **DO NOT WRAP** to an arbitrary column size ; editors soft-wrap. Editing \n wrapped paragraphs is a pain.

## Comment style

Two separate rules. Which applies depends on whether the reader can see the implementation.

### Doc comments on golang exported identifiers (and the package doc)

The reader is in godoc or an editor hover, not the source. The comment is the whole contract. No length cap — as long as the contract needs.

- Start with the identifier's name; first sentence is a complete summary that stands alone (it's what package listings and search show).
- State what callers must know and can't infer from the signature: preconditions, which error conditions are distinguishable, concurrency
  safety, who owns/closes returned resources, whether an argument is retained or aliased, nil handling, whether the zero value is usable, whether it blocks or does I/O.
- State the contract directly. Do NOT substitute a spec citation for it — an external consumer can't follow a repo-relative link. Cite the spec in addition, never instead.
- Keep implementation rationale OUT. Why this approach beat another, what the internals do — inline, not here.
- Methods satisfying an interface may defer: "// Read implements io.Reader."

### Inline comments, and unexported identifiers

The reader can see the code. Comment only non-obvious decisions, invariants, and ordering constraints. Two lines each. State the constraint and its reason; stop. NEVER cite or point to a spec or a doc.

- Don't explain standard library or language semantics.
- Don't enumerate the consequences of violating the constraint.
- Don't argue against alternatives unless one was tried and broke.
- No parentheticals inside parentheticals.
- Unexported identifiers get a doc comment only where the name doesn't carry it.

# Golang code

- Errors MUST use github.com/samber/oops and be provided relevant context. Always forward/wrap as needed.
- JSON parsing uses github.com/bytedance/sonic/ast (query/expression_parse.go). When reading a value off an ast.Node where the JSON type matters (deciding what kind of thing a value is, not just extracting it once its type is already known), use the Strict* accessors (StrictString/StrictBool/StrictFloat64/...), never the lenient String()/Bool()/Float64() ones — those coerce across JSON types (a JSON number's .String() silently returns "42"), which will silently corrupt any tag-dispatch or type-detection logic built on top of them.
- All JSON work MUST be done with github.com/bytedance/sonic

# Typescript

- Always use `just check` : NO error MUST remain
