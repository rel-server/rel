---
icon: material/file-code-outline
---

# Rendering HTML with templates

A route can render server-side HTML instead of returning JSON, without giving up rel's own
security posture: set `template` on the `RelHttpResponse` (see [Requests and
responses](requests-responses.md)) and rel renders it with
[Jet](https://github.com/CloudyKit/jet), a Go template engine, instead of serializing
`content`.

```sql
return jsonb_build_object(
  'status', 200,
  'content_type', 'text/html',
  'template', 'booking-confirmed.jet',
  'template_data', jsonb_build_object('guest_name', guest_row.first_name)
);
```

`template` is a path relative to `http.templates.path` (default `/template`; see
[Configuration](../configuration/index.md)). `content` is ignored whenever `template` is set —
not an error to set both, just pointless. A template that fails to load (missing file, parse
error) or fails during execution (referencing a field `Data`/`Req` doesn't have) is a `500`,
logged with the template path, never a silent fallback to `content`.

## What a template can see

The template executes with three named variables in scope:

| Variable | Value |
|---|---|
| `Data` | `template_data` from the response, JSON `null` if unset. |
| `Req` | The exact `RelHttpRequest` the route function itself received — `{{ Req.uri }}`, `{{ Req.jwt.role }}`. |
| `Nonce` | The request's CSP nonce (`Req.csp_nonce`) — see below. |

`content_type` is never implied by `template` — a route rendering HTML via a template still
sets `content_type: "text/html"` itself, the same as if it had built the HTML string by hand.

## `Nonce`: trusted inline scripts without loosening the CSP

This is the reason templates exist as more than a convenience. rel generates a fresh,
cryptographically random nonce for every request and enforces a `Content-Security-Policy` that
by default blocks inline `<script>`/`<style>` entirely (see [CORS and CSP](cors-csp.md)).
`{{ Nonce }}` is how a template opts a specific inline block back in, without disabling the
policy for the whole response:

```html
<!-- template/booking-confirmed.jet -->
<h1>Booking confirmed for {{ Data.guest_name }}</h1>
<script nonce="{{ Nonce }}">/* trusted inline code */</script>
```

rel appends `'nonce-<value>'` to the response's `script-src`/`style-src` CSP directives to
match — the two always agree, so a nonce copied out of `Req.csp_nonce` into hand-built HTML and
a nonce read from `{{ Nonce }}` in a template are equally valid. A `<script>`/`<style>` tag
without a matching nonce (or without `'unsafe-inline'` explicitly configured) simply doesn't
run, browser-enforced — this is what makes returning raw HTML from a route safe by default
even though the response is dynamic.

Jet auto-escapes every `{{ value }}` interpolation for HTML; the one place that escaping is
wrong is inside a `<script>` block, since HTML-escaping a value doesn't produce valid or safe
JavaScript. Pass dynamic data into an inline script through a JSON island instead of
interpolating it directly:

```html
<script nonce="{{ Nonce }}" type="application/json" id="data">{{ json(Data) | raw }}</script>
```

`json(...)` is one of Jet's built-in globals — `encoding/json.Marshal` under the hood, already
escaped correctly for a JSON-typed `<script>` body (Go's JSON encoder escapes `<`, `>`, and `&`
inside strings, so a `</script>`-shaped value can't break out of the block). `| raw` is
required on top of it: piping already-correct JSON through Jet's default HTML escaper would
mangle those escapes a second time.

## Layouts: `extends`, `block`, and `yield`

Jet templates can extend one another, the same inheritance model as Django or Twig: a base
layout declares named blocks, and a page extending it overrides the ones it wants to fill in.

```html
<!-- template/layout.jet -->
<!doctype html>
<html>
<head><title>{{ block "title" }}rel{{ end }}</title></head>
<body>
  <script nonce="{{ Nonce }}">/* shared boilerplate */</script>
  {{ block "content" }}{{ end }}
</body>
</html>
```

```html
<!-- template/booking-confirmed.jet -->
{{ extends "layout.jet" }}
{{ block "title" }}Booking confirmed{{ end }}
{{ block "content" }}
  <h1>Booking confirmed for {{ Data.guest_name }}</h1>
{{ end }}
```

`{{ extends "layout.jet" }}` resolves the same way `template` itself does — relative to
`http.templates.path` — so a layout used by several routes' templates lives alongside them in
that same directory tree, not somewhere separate. `Data`, `Req`, and `Nonce` stay in scope
across the whole chain: a value read in a block inherited from the base layout is exactly the
`template_data`/request the route itself set, not something the child template has to
re-thread through.

`{{ import "partials.jet" }}` (reusable macros, shared across many templates without an
inheritance relationship) and `{{ include "footer.jet" }}` (inline another template's full
output at that point) are both available too, resolved the same way.

## Reload

The template set (and its parse cache) rebuilds as part of the same `SIGUSR1` reload sequence a
schema change uses — see [Operations](../configuration/operations.md). A changed `.jet` file on
disk takes effect the next time an operator reloads, not on every request; there is no
development mode that reparses from disk on every render.
