# Dmut dockerfile

`Dockerfile.dmut` builds both `rel` (from this repo) and `github.com/ceymard/dmut/v2@v2.0.5` from source in its own build stage, as static, CGO-disabled binaries, mirroring `Dockerfile`'s build stage. It installs both at `/` (`/rel`, `/dmut`) and is buildable standalone with a plain `docker build`, independently of the release pipeline.

> **Why:** dmut is pure Go, so it can run from the same `scratch` final stage as `rel` (no shell, no libc).

The final image presets `reload.cmd` to:

```
REL_RELOAD__CMD=/dmut apply "postgres://{DMUT_USER:pg.user:user}:{DMUT_PASSWORD:pg.password:password}@{pg.host}/{pg.database}" "{DMUT_MUTATIONS_PATH:/sql}"
```

`reloadcmd`'s placeholder syntax (reloadcmd/run.go) is extended from `{name}`/`{name:default}` to a `:`-separated chain of any length: every segment but the last is a name resolved by the existing rule (dotted → config key from `cfg.Raw`, undotted → environment variable) ; the last segment is always a literal, never resolved as a name. Substitution uses the first segment that resolves to a non-empty value ; a name that is unset, or resolves to an empty string, is treated as not found and the chain moves to the next segment. `{name:default}` keeps working exactly as before, since it's a chain of length two.

`DMUT_USER` and `DMUT_PASSWORD` are plain environment variables set by whoever runs the image, not `REL_`-prefixed config. Left unset, they fall back to rel's own `pg.user`/`pg.password`.

`DMUT_MUTATIONS_PATH` (default `/sql`) holds the migration files dmut applies. It is not declared as a Dockerfile `VOLUME` : bind-mounted over during development, its contents are copied into the image for deployment. dmut only reads from it and only opens outbound network connections to Postgres — no other filesystem writes — so it runs as the same non-root `USER 1000:1000` as rel.

The image is published to `ghcr.io/rel-server/rel-dmut`, not Docker Hub. It is wired into `.goreleaser.yaml`'s `dockers:` section, using `docker buildx build --platform=$BUILDPLATFORM` with `GOOS=$TARGETOS`/`GOARCH=$TARGETARCH` inside the build stage for multi-arch cross-compilation, rather than `Dockerfile.goreleaser`'s pattern of taking a precompiled `rel` binary as build context. A `justfile` command also builds and pushes it.

Both `ghcr.io/rel-server/rel` and `ghcr.io/rel-server/rel-dmut` publish on two triggers:

- Pushing a `v*` git tag runs the existing GoReleaser release pipeline, tagging both images `{{.Version}}` and `latest`.
- Pushing to the `main` branch builds both images directly with `docker buildx build --push`, from `Dockerfile` and `Dockerfile.dmut` respectively (no GoReleaser involved), tagging them `main`. Each push to `main` overwrites that same tag, so there is always exactly one rolling `main` image per repo, never one per commit.

`rel`'s own image keeps using `Dockerfile.goreleaser` (precompiled binary from GoReleaser's own cross-compiled build) for the tag-triggered path, since that avoids rebuilding `rel` a second time there ; the `main`-triggered path uses the plain `Dockerfile` instead, since it isn't running inside GoReleaser.

`.github/workflows/release.yml` authenticates to `ghcr.io` (via `GITHUB_TOKEN`, `packages: write` permission) instead of Docker Hub.
