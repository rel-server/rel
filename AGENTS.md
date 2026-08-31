# General instructions

- ALWAYS Create regression and integration tests using either plain golang testing or testcontainers with a sample postgres database.
- Test your claims/assertions instead of only recalling when your confidence level is not impeccable
- DRY : keep the code simple, avoid repetitions and factorize code whenever possible.
- Implementations MUST always be the most efficient CPU/RAM wise. If a compromise is to be made, prompt the user
- Always prompt the user whenever you deem an important architectural decision is to be made (adding/removing a library, implementation details/philosophy, performance concerns)
- Explain the code through comments when implementing
- If there is a TODO.md file somewhere, keep it updated with what's been done
- When replying or writing specs / docs / comments, use plain english over lingo and buzzwords ; stay clear and legible by non-senior developers.
- When alerting me on problems or inconsistencies, use examples if the explanation is complex

- Maintain `./docs` <-> code relevance

# When writing specs or code

In specs and docs, create links between files.

The redactor(s) write specs with you as a mirror, to help shape them as best as possible for a prompt implementation by a low/medium thinking agent.

Spec language MUST be specification-only : no musing, rationale, or back-and-forth outside a blockquote. Everything outside a blockquote is a binding rule. A blockquote is optional context — skip it when implementing, and consult it only when a rule seems ambiguous or you want to check a judgment call. No remnant of our conversation may remain outside a blockquote ; code blocks are the one exception, where explanatory inline comments stay regardless.

Blockquote types :

- `> Why:` — rationale/justification for the rule immediately above it.
- `> Question:` — a lingering question you need answered. Remove it once answered (in the text, or during conversation) ; amend it in place if the answer isn't sufficient yet.
- `> Thoughts:` — your own scratch reasoning. The redactor deletes these by default ; delete one yourself only once it's gone obsolete (superseded, or its question already resolved elsewhere).
- `> Advise:` — an explicit question from the redactor to you, however they label it (`Advise`, or whatever they happen to reach for in the moment — treat any clearly question-directed custom blockquote the same way). When you reply, delete the block itself, leaving the updated spec text in its place, plus any `> Thoughts:`/`> Question:` you want to leave behind.
- >: the user is directly talking to you

The redactor may also leave a question inline, outside any blockquote (e.g. a parenthetical) while redacting, for commodity. Address it like if it were `> Advise:`.

# When writing code

Similarly to spec work ; leave questions/dialogue with a marker, like //> Question: so that I can find items to go back to more easily by grepping.

# Golang code

- Errors MUST use github.com/samber/oops and be provided relevant context. Always forward/wrap as needed.
- JSON parsing uses github.com/bytedance/sonic/ast (query/expression_parse.go). When reading a value off an ast.Node where the JSON type matters (deciding what kind of thing a value is, not just extracting it once its type is already known), use the Strict* accessors (StrictString/StrictBool/StrictFloat64/...), never the lenient String()/Bool()/Float64() ones — those coerce across JSON types (a JSON number's .String() silently returns "42"), which will silently corrupt any tag-dispatch or type-detection logic built on top of them.
- All JSON work MUST be done with github.com/bytedance/sonic

# Typescript

- Always use `just check` : NO error MUST remain
