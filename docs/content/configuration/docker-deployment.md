---
icon: simple/docker
---

# Docker deployment

rel ships as a small, non-root, scratch-based image (`ceymard/rel`), listening on `8080` and
reading every setting from the environment the way [Configuration](index.md) describes. This
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
    image: ceymard/rel:latest
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
      - ./dmut:/dmut:ro
      - ./wellknown:/wellknown:ro

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
  [Authentication](authentication.md)). Set both to the same externally-visible domain, or
  OIDC/SAML logins will redirect somewhere wrong even though plain `/rel`/`/route` traffic
  works fine.
- **`VIRTUAL_PORT` is optional here** — the image only `EXPOSE`s `8080`, so `nginx-proxy`
  finds it automatically — but it's cheap to be explicit, and it stops being optional the
  moment more than one port is ever exposed on the same container.
- **Mount `/secrets` on a named volume, not the container's writable layer.** rel generates
  `jwt.secret` (and the SAML SP certificate/key, if configured) into `/secrets` on first boot
  and reuses them after — losing that volume on a redeploy silently invalidates every session
  and, for SAML, every IdP trust relationship. See [Secrets and generated
  values](index.md#secrets-and-generated-values).
- **`/dmut` and `/wellknown` are read-only bind mounts of files from your own repo** — dmut
  migration files and well-known query definitions aren't something the container generates,
  they're deployed alongside it. `/static` and `/template` (not shown above) follow the same
  pattern if you're serving static files or Jet templates.
- `nginx-proxy` and `acme-companion` need to see the Docker socket to discover containers and
  their `VIRTUAL_HOST`/`LETSENCRYPT_*` labels — that's what the `docker.sock` mount is for,
  not something rel itself needs or sees.

The compose file above assumes `ceymard/rel:latest` is already sitting in a registry
`docker-compose pull`/`docker-compose up` can reach. Build it yourself with `just image`
(tags it `ceymard/rel:<version>` and `:latest`, `<version>` from `git describe`), and
`just upload` to push both tags once you've logged in to your registry of choice.
