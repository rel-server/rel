# General instructions

- ALWAYS Create regression and integration tests using either plain golang testing or testcontainers with a sample postgres database.
- DRY : keep the code simple, avoid repetitions and factorize code whenever possible.
- Implementations MUST always be the most efficient CPU/RAM wise. If a compromise is to be made, prompt the user
- Always prompt the user whenever you deem an important architectural decision is to be made (adding/removing a library, implementation details/philosophy, performance concerns)
- Explain the code through comments when implementing

- Maintain `./docs` <-> code relevance
- In specs, put rationale/justification for a rule in a blockquote right after it, starting with `> Why:`. Treat everything outside such a blockquote as a binding rule ; treat the blockquote itself as optional context to skip when implementing, and consult only when a rule seems ambiguous or you want to check a judgment call. Consider you're the target audience (besides the > Why), instructions and questions may be submitted to you in the text ; address these. For lingering questions you need answers to, use `> Question:`.

# Golang code

- Errors MUST use github.com/samber/oops and be provided relevant context. Always forward/wrap as needed.

# Typescript

- Always use `just check` : NO error MUST remain
