---
icon: material/docker
---

# Docker deployment

rel ships as a small, non-root, scratch-based image (`rel-server/rel`), listening on `8080` and
reading every setting from the environment the way [Configuration](../configuration/index.md) describes. This
walks through a realistic deployment: rel and Postgres in Docker, fronted by
`jwilder/nginx-proxy` — a reverse proxy that watches the Docker socket and routes by a
container's own `VIRTUAL_HOST` label — with automatic TLS via its companion,
`nginxproxy/acme-companion`.

`nginx-proxy` needs no rel-specific configuration beyond the usual: put both containers on the
same Docker network and give rel a `VIRTUAL_HOST`.

```yaml
# docker-compose.yml
services:
  nginx-proxy:
    image: jwilder/nginx-proxy
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - /var/run/docker.sock:/tmp/docker.sock:ro
      - certs:/etc/nginx/certs
      - vhost:/etc/nginx/vhost.d
      - html:/usr/share/nginx/html

  # Optional: issues and renews Let's Encrypt certificates for every
  # container nginx-proxy routes to, driven by the same LETSENCRYPT_*
  # labels below. Drop this service (and the LETSENCRYPT_* env vars on
  # rel) to run HTTP-only, e.g. behind another TLS-terminating layer.
  acme-companion:
    image: nginxproxy/acme-companion
    restart: unless-stopped
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - certs:/etc/nginx/certs
      - vhost:/etc/nginx/vhost.d
      - html:/usr/share/nginx/html
      - acme:/etc/acme.sh

  db:
    image: postgres:16-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER: hotel
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: hotel
    volumes:
      - pgdata:/var/lib/postgresql/data

  rel:
    image: your-registry/your-app:latest
    restart: unless-stopped
    depends_on:
      - db
    environment:
      REL_PG__URI: postgres://hotel:${DB_PASSWORD}@db:5432/hotel
      REL_HTTP__PUBLIC_HOST: api.example.com
      VIRTUAL_HOST: api.example.com
      VIRTUAL_PORT: "8080"
      LETSENCRYPT_HOST: api.example.com
      LETSENCRYPT_EMAIL: ops@example.com
    volumes:
      - rel_secrets:/secrets

volumes:
  certs:
  vhost:
  html:
  acme:
  pgdata:
  rel_secrets:
```

A few things worth getting right:

- **`VIRTUAL_HOST` and `REL_HTTP__PUBLIC_HOST` should match.** `nginx-proxy` uses
  `VIRTUAL_HOST` to route incoming requests to the container; rel uses `http.public_host` to
  build its own redirect/callback URLs for OpenID Connect and SAML (see
  [Authentication](../http/authentication.md)). Set both to the same externally-visible domain, or
  OIDC/SAML logins will redirect somewhere wrong even though plain `/rel`/`/route` traffic
  works fine.
- **`VIRTUAL_PORT` is optional here** — the image only `EXPOSE`s `8080`, so `nginx-proxy`
  finds it automatically — but it's cheap to be explicit, and it stops being optional the
  moment more than one port is ever exposed on the same container.
- **Mount `/secrets` on a named volume, not the container's writable layer.** rel generates
  `jwt.secret` (and the SAML SP certificate/key, if configured) into `/secrets` on first boot
  and reuses them after — losing that volume on a redeploy silently invalidates every session
  and, for SAML, every IdP trust relationship. See [Secrets and generated
  values](../configuration/index.md#secrets-and-generated-values). This is the *only* directory rel itself
  writes runtime state to, and the only one that belongs on a volume — see below.
- `nginx-proxy` and `acme-companion` need to see the Docker socket to discover containers and
  their `VIRTUAL_HOST`/`LETSENCRYPT_*` labels — that's what the `docker.sock` mount is for,
  not something rel itself needs or sees.

## `/wellknown`, `/static`, `/template`, and reload.cmd: build them into your own image

Well-known query definitions, static assets, and Jet templates aren't runtime state — they're
part of what version of your app is running, exactly like the schema they query against.
Bind-mounting them from the host (`./wellknown:/wellknown:ro`) works for local development,
but for a real deployment, build your own image `FROM rel-server/rel` and `COPY` them in
instead:

```dockerfile
FROM rel-server/rel:latest
COPY wellknown /wellknown
COPY static /static
COPY template /template
```

The same goes for whatever `reload.cmd` itself needs — a migration tool's binary and its own
migration files, say. `reload.cmd` names its own path directly (see [Reload](../configuration/reload.md)), so
there's no fixed default directory to document here ; `COPY` it in at whatever path you choose,
alongside `reload.cmd`'s own config value naming that path.

Tag and deploy that image (`your-registry/your-app:<version>`) the same way you would any other
build artifact — a redeploy rolls forward and back by changing one tag, and there's no separate
"did the host's bind-mounted files actually match the image that's running" question to answer
during an incident. The compose file above deploys this way: `image: your-registry/your-app`,
not `rel-server/rel` directly, and no `/wellknown`/`/static`/`/template` volumes at all.

## Combining baked-in static assets with writable uploads

`http.static.path` accepts several colon-separated directories and serves them as one merged
`/static/*` tree (see [Static files](../http/static-files.md)); a [file
upload](../http/uploads.md) landing on disk without routing through Postgres always writes
under the *first* one specifically. That ordering is also the tool for combining assets baked
into your image with a writable upload directory, without any dedicated upload configuration:
put the writable volume first, your `COPY`'d assets second.

```dockerfile
# in your app's Dockerfile
COPY static /static/assets
```

```yaml
# in your compose file
environment:
  REL_HTTP__STATIC__PATH: /uploads:/static/assets
volumes:
  - rel_uploads:/uploads
```

Uploads land in `/uploads`, on a volume that survives a redeploy; `/static/assets` is whatever
version of your app's own static files the currently-running image was built with. There's
deliberately no separate `http.uploads.path` setting or default `/uploads` volume in the base
image — which directory in the list is writable, if any, is a decision about your app's own
deployment, not something rel's base image should assume for you.

The compose file above assumes `your-registry/your-app:latest` is already sitting in a registry
`docker-compose pull`/`docker-compose up` can reach. `just image` builds the plain
`rel-server/rel` base image (tags it `rel-server/rel:<version>` and `:latest`, `<version>` from `git
describe`) and `just upload` pushes it — useful as the `FROM` your own app's image builds on
top of, not something you deploy directly once you have app-specific assets to bake in.
