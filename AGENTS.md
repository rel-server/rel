# General instructions

- ALWAYS Create regression and integration tests using either plain golang testing or testcontainers with a sample postgres database.
- Test your claims/assertions instead of only recalling when your confidence level is not impeccable
- DRY : keep the code simple, avoid repetitions and factorize code whenever possible.
- Implementations MUST always be the most efficient CPU/RAM wise. If a compromise is to be made, prompt the user
- Always prompt the user whenever you deem an important architectural decision is to be made (adding/removing a library, implementation details/philosophy, performance concerns)
- Explain the code through comments when implementing

- Maintain `./docs` <-> code relevance


# When writing specs

The redactor(s) write specs with you as a mirror, to help shape them as best as possible for a prompt implementation by a low/medium thinking agent.

Spec language MUST be specification-only : no musing, rationale, or back-and-forth outside a blockquote. Everything outside a blockquote is a binding rule. A blockquote is optional context — skip it when implementing, and consult it only when a rule seems ambiguous or you want to check a judgment call. No remnant of our conversation may remain outside a blockquote ; code blocks are the one exception, where explanatory inline comments stay regardless.

Blockquote types :

- `> Why:` — rationale/justification for the rule immediately above it.
- `> Question:` — a lingering question you need answered. Remove it once answered (in the text, or during conversation) ; amend it in place if the answer isn't sufficient yet.
- `> Thoughts:` — your own scratch reasoning. The redactor deletes these by default ; delete one yourself only once it's gone obsolete (superseded, or its question already resolved elsewhere).
- `> Advise:` — an explicit question from the redactor to you, however they label it (`Advise`, or whatever they happen to reach for in the moment — treat any clearly question-directed custom blockquote the same way). When you reply, delete the block itself, leaving the updated spec text in its place, plus any `> Thoughts:`/`> Question:` you want to leave behind.

The redactor may also leave a question inline, outside any blockquote (e.g. a parenthetical) while redacting, for commodity. Address it like if it were `> Advise:`.

# Golang code

- Errors MUST use github.com/samber/oops and be provided relevant context. Always forward/wrap as needed.

# Typescript

- Always use `just check` : NO error MUST remain
