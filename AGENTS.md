# General instructions

- ALWAYS Create regression and integration tests using either plain golang testing or testcontainers with a sample postgres database.
- DRY : keep the code simple, avoid repetitions and factorize code whenever possible.
- Implementations MUST always be the most efficient CPU/RAM wise. If a compromise is to be made, prompt the user
- Always prompt the user whenever you deem an important architectural decision is to be made (adding/removing a library, implementation details/philosophy, performance concerns)
- Explain the code through comments when implementing

- Maintain `./docs` <-> code relevance


# When writing specs

- Language MUST be specification-only with no musing. No remnants of our conversations must remain outside of specific blocks. The redactor may leave questions in the middle of the text that you are to address but ultimately remove.
- You or an Agent are the target audience for implementation.
- For continued conversations between you and the redactor, use blockquotes. Treat everything outside such a blockquote as a binding rule ; treat the blockquote itself as optional context to skip when implementing, and consult only when a rule seems ambiguous or you want to check a judgment call. 
  - Put rationale/justification for a rule in a blockquote right after it, starting with `> Why:`. 
  - For lingering questions you need answers to, use `> Question:`.
  - You may leave your thought process in `> Thoughts:` blocks.

# Golang code

- Errors MUST use github.com/samber/oops and be provided relevant context. Always forward/wrap as needed.

# Typescript

- Always use `just check` : NO error MUST remain
