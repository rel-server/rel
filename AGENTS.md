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

- Maintain `./docs` <-> code relevance

# When authoring


In specs and docs, create links between files with [[wiki]] syntax.

The redactor(s) write specs with you as a mirror, to help shape them as best as possible for a prompt implementation by a low/medium thinking agent.


Spec language MUST be specification-only : no musing, rationale, or back-and-forth outside a blockquote. Everything outside a blockquote is a binding rule. A blockquote is optional context — skip it when implementing, and consult it only when a rule seems ambiguous or you want to check a judgment call. No remnant of our conversation may remain outside a blockquote ; code blocks are the one exception, where explanatory inline comments stay regardless.

Blockquote types :

- `> Why:` — rationale/justification for the rule immediately above it, only for rules that appear intentional or non-trivial that clearly raise eyebrows.
- `> Question:` — a lingering question you need answered. Remove it once answered (in the text, or during conversation) ; amend it in place if the answer isn't sufficient yet.
- `> Thoughts:` — your own scratch reasoning. The redactor deletes these by default ; delete one yourself only once it's gone obsolete (superseded, or its question already resolved elsewhere).
- `> Advise:` — an explicit question from the redactor to you, however they label it (`Advise`, or whatever they happen to reach for in the moment — treat any clearly question-directed custom blockquote the same way). When you reply, delete the block itself, leaving the updated spec text in its place, plus any `> Thoughts:`/`> Question:` you want to leave behind.
- >: the user is directly talking to you

The redactor may also leave a question inline, outside any blockquote (e.g. a parenthetical) while redacting, for commodity. Address it like if it were `> Advise:`.

Style constraints on spec text (outside blockquotes):

- **No self-narration.** Never describe how the spec came to say what it says — "this session's own convention," "an earlier draft had X," "corrected here rather than left to drift," "not an oversight." State the current rule only; don't narrate its history, not even in a `> Why:`.
- **One rule, one sentence.** If a bullet needs "not X, not Y either, but Z" hedging to land, the justification has leaked into the rule. Rewrite as a flat positive statement. Move the "why not X" reasoning to `> Why:`, or drop it if it isn't needed to resolve a real ambiguity.
- **Rationale is opt-in reading, not load-bearing.** A rule must be fully implementable with every blockquote stripped from the doc. Test literally: if deleting all `>` blocks removes information needed to implement correctly, the split has failed — move that content out of the blockquote and into the rule, or accept it's optional context.
- **Cross-references are citations, not sentences.** `` `specs/foo.md ## Bar` `` terminates a clause ; it doesn't spawn a subordinate clause explaining why that section is relevant.

# When writing code

Similarly to spec work ; leave questions/dialogue with a marker, like //> Question: so that I can find items to go back to more easily by grepping.

## Comment style

Two separate rules. Which applies depends on whether the reader can see
the implementation.

### Doc comments on golang exported identifiers (and the package doc)

The reader is in godoc or an editor hover, not the source. The comment is
the whole contract. No length cap — as long as the contract needs.

- Start with the identifier's name; first sentence is a complete summary
  that stands alone (it's what package listings and search show).
- State what callers must know and can't infer from the signature:
  preconditions, which error conditions are distinguishable, concurrency
  safety, who owns/closes returned resources, whether an argument is
  retained or aliased, nil handling, whether the zero value is usable,
  whether it blocks or does I/O.
- State the contract directly. Do NOT substitute a spec citation for it —
  an external consumer can't follow a repo-relative link. Cite the spec in
  addition, never instead.
- Keep implementation rationale OUT. Why this approach beat another, what
  the internals do — inline, not here.
- Methods satisfying an interface may defer: "// Read implements io.Reader."

### Inline comments, and unexported identifiers

The reader can see the code. Comment only non-obvious decisions,
invariants, and ordering constraints. Two lines each. State the
constraint and its reason; stop.

- Don't explain standard library or language semantics.
- Don't restate what a referenced spec section, doc comment, or nearby
  log message already says — cite it instead.
- Don't enumerate the consequences of violating the constraint.
- Don't argue against alternatives unless one was tried and broke.
- No parentheticals inside parentheticals.
- Unexported identifiers get a doc comment only where the name doesn't
  carry it. If a fact belongs in specs/, put it in specs/ and cite it.

# Golang code

- Errors MUST use github.com/samber/oops and be provided relevant context. Always forward/wrap as needed.
- JSON parsing uses github.com/bytedance/sonic/ast (query/expression_parse.go). When reading a value off an ast.Node where the JSON type matters (deciding what kind of thing a value is, not just extracting it once its type is already known), use the Strict* accessors (StrictString/StrictBool/StrictFloat64/...), never the lenient String()/Bool()/Float64() ones — those coerce across JSON types (a JSON number's .String() silently returns "42"), which will silently corrupt any tag-dispatch or type-detection logic built on top of them.
- All JSON work MUST be done with github.com/bytedance/sonic

# Typescript

- Always use `just check` : NO error MUST remain
