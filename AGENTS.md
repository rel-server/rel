# General instructions

- ALWAYS Create regression and integration tests using either plain golang testing or testcontainers with a sample postgres database (fixture conventions: `specs/testing.md`). Only test project-specific behavior, business rules and edge cases that could realistically break; never the language, the browser or a library, trivial getters/setters, or a constant against its own definition. A test's title is a claim its assertions must prove. Check the behavior isn't already tested elsewhere before adding a test.
- Fix root causes instead of patching around structural problems ; if the fix is structurally important, ask the user before making it
- Test your claims/assertions instead of only recalling when your confidence level is not impeccable
- DRY : keep the code simple and avoid repetitions.
  - Before writing a helper, search for an existing one that does the same thing.
  - Before writing a feature, trace how the closest existing feature does the same job, across functions and packages, and build on that path when the job is the same. If it would need restructuring to fit, ask first. In your reply, say which path you reused, or why you did not.
  - When you extend shared code, extract the common part rather than add a parameter that every caller sets to a constant. Existing callers must keep their behaviour.
- Implementations MUST always be the most efficient CPU/RAM wise. If a compromise is to be made with the previous rule, prompt the user
- Always prompt the user before an important architectural decision (adding/removing a dependency, implementation details/philosophy, performance concerns). NEVER add a dependency by yourself.
- Use the domain terms from `CONTEXT.md`
- Comments are terse. Only comment project-specific behaviour, business rules and edge cases ; state the constraint and its reason, then stop. Never explain the language or a library, never list what breaks if the rule is violated, never argue against alternatives unless one was tried and failed, never cite or link a spec/doc.
- Function comments: one or two sentences. Exported identifiers are the exception: state what the caller cannot see in the signature (distinguishable errors, ownership, concurrency, nil/zero value), not how it works.
- If there is a TODO.md file somewhere, keep it updated with what's been done
- When replying or writing specs / docs / comments, use plain english, no lingo, no buzzwords.


- ALWAYS Update docs (`docs/content`) as you work on the code : docs MUST always be relevant.
- NEVER rm -rf previously produced docs (`docs/site`) when testing them with zensical ; it messes up the live server
- In markdown, **DO NOT WRAP** to an arbitrary column size ; editors soft-wrap. Editing \n wrapped paragraphs is a pain.
- run go vet ./... and just test before declaring done

# Golang code

- Errors MUST use github.com/samber/oops and be provided relevant context. Always forward/wrap as needed.
- JSON parsing uses github.com/bytedance/sonic/ast (query/expression_parse.go). When reading a value off an ast.Node where the JSON type matters (deciding what kind of thing a value is, not just extracting it once its type is already known), use the Strict* accessors (StrictString/StrictBool/StrictFloat64/...), never the lenient String()/Bool()/Float64() ones — those coerce across JSON types (a JSON number's .String() silently returns "42"), which will silently corrupt any tag-dispatch or type-detection logic built on top of them.

# Typescript

- Always use `just check` : NO error MUST remain
