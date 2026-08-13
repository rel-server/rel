# General instructions

- ALWAYS Create regression and integration tests using either plain golang testing or testcontainers with a sample postgres database.
- DRY : keep the code simple, avoid repetitions and factorize code whenever possible.
- Implementations MUST always be the most efficient CPU/RAM wise. If a compromise is to be made, prompt the user
- Always prompt the user whenever you deem an important architectural decision is to be made (adding/removing a library, implementation details/philosophy, performance concerns)
- Explain the code through comments when implementing

- Maintain `./docs` <-> code relevance

# Golang code

- Errors MUST use github.com/samber/oops and be provided relevant context. Always forward/wrap as needed.

# Typescript

- Always use `just check` : NO error MUST remain
