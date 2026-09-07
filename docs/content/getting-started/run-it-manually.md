---
icon: material/tools
---

# Run it manually

Three ways to get the `rel` binary itself running, pointed at whatever Postgres database you
already have — pick whichever fits: a prebuilt release binary, a Docker container, or a build
from source.

## Download a prebuilt binary

Every tagged release publishes `linux`/`darwin`, `amd64`/`arm64` archives on
[GitHub Releases](https://github.com/rel-server/rel/releases):

```sh
curl -L -o rel.tar.gz https://github.com/rel-server/rel/releases/latest/download/rel_<version>_<os>_<arch>.tar.gz
tar xzf rel.tar.gz
./rel --pg.uri "postgres://user:pass@localhost:5432/mydb"
```

Replace `<version>`/`<os>`/`<arch>` with the release and platform you want (e.g.
`rel_1.2.0_linux_amd64.tar.gz`) — the exact archive names are listed on each release's own page.

## Run it with Docker manually

The same binary, as a small non-root, scratch-based image — no compose file, no reverse proxy,
just a container listening on `8080`:

```sh
docker run -p 8080:8080 -e REL_PG__URI="postgres://user:pass@host.docker.internal:5432/mydb" rel-server/rel
```

Every setting is an environment variable (`REL_<SECTION>__<KEY>`) the same way it is anywhere
else — see [Configuration](../configuration/index.md). `host.docker.internal` reaches a
Postgres running on the host itself from inside the container; point at a real host/container
name instead if Postgres is already running in Docker too. See [Docker
deployment](docker-deployment.md) for a realistic multi-container setup — Postgres alongside
rel, a reverse proxy, and automatic TLS.

## Build from source

Needs Go installed locally; there's no separate `go install`-able module path, so building from
a checked-out copy of the repo is the only supported source build:

```sh
git clone https://github.com/rel-server/rel.git
cd rel
go build -o rel ./cmd/rel
./rel --pg.uri "postgres://user:pass@localhost:5432/mydb"
```

See [Launch it yourself](../example-database/launch-it-yourself.md) for building from source
*and* standing up this repo's own dev fixture database at the same time — the quickest way to
have something real to point rel at while you read the rest of these docs.
