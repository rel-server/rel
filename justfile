# test idents
db_container := "rel-dev-db"
db_name := "hotel"
db_user := "hotel"
db_password := "test"

image_registry := "ceymard/rel"
version := `git describe --tags --always --dirty`

# Run the full test suite (testcontainers spins up its own throwaway Postgres
# per package — docker must be running, but `just db-up` is not required)
test:
    go test ./...

# typecheck and biome check
check:
    cd typescript && bunx tsc --noEmit && biome check

# Launch the rel server against the dev database (just db-up first). Uses
# pg.uri alone — REL_PG__QUERY__USER/PASSWORD are unset (there's only one
# role in this dev fixture) ; a real deployment would set those separately,
# pg.query deliberately less privileged than the primary login.
run:
    REL_PG__URI="$(just db-uri)" go run ./cmd/rel

# Launch a persistent Postgres dev database for manual testing (docker only, dynamic port)
db-up:
    docker run -d --name {{db_container}} \
        -e POSTGRES_USER={{db_user}} -e POSTGRES_PASSWORD={{db_password}} -e POSTGRES_DB={{db_name}} \
        postgres:16-alpine >/dev/null
    @echo -n "waiting for postgres..."
    @until docker exec {{db_container}} pg_isready -U postgres >/dev/null 2>&1; do sleep 1; done
    @echo " ready at $(just db-uri)"

# Stop and remove the dev database. Safe to run even if it isn't up.
db-down:
    docker rm -f {{db_container}} >/dev/null 2>&1 || true

# Print the connection URI for the running dev database
db-uri:
    @docker inspect {{db_container}} --format 'postgres://{{db_user}}:{{db_password}}@{{"{{"}}.NetworkSettings.Networks.bridge.IPAddress}}/{{db_name}}?sslmode=disable'

# Apply the dmut schema mutations (test/dmut) to the dev database
db-migrate:
    dmut apply "$(just db-uri)" test/dmut

# Seed the dev database with fake data (test/seed)
db-seed:
    go run ./test/seed "$(just db-uri)"

# One-shot : tear down, bring up fresh, migrate, seed — left running afterwards
db-fresh: db-down db-up db-migrate db-seed
    @echo "fresh dev database ready at $(just db-uri)"

# Open a psql shell into the dev database
db-psql:
    docker exec -it {{db_container}} psql -U postgres -d {{db_name}}

# Build the scratch-based server image, tagged with the current git version
# (git describe : the checked-out tag, or <tag>-N-g<sha>[-dirty] otherwise)
# and "latest".
image:
    docker build --build-arg VERSION={{version}} \
        -t {{image_registry}}:{{version}} \
        -t {{image_registry}}:latest \
        .

# Push the image built by `just image` (both tags) to image_registry — log
# in first (e.g. `docker login ghcr.io`).
upload: image
    docker push {{image_registry}}:{{version}}
    docker push {{image_registry}}:latest

# --- Documentation (zensical, versioned with the Zensical fork of mike) ---
# The whole doc site — config, content, template overrides — lives under
# ./docs ; docs/zensical.toml is the project root `-f`/`-F` points at, never
# the repo root itself.

docs_venv := ".venv-docs"
docs_config := "docs/zensical.toml"

# Create/refresh the docs tooling virtualenv (zensical + squidfunk/mike, the
# Zensical-compatible fork — not published on PyPI, installed from GitHub)
docs-install:
    uv venv {{docs_venv}} 2>/dev/null || true
    uv pip install --python {{docs_venv}}/bin/python zensical
    uv pip install --python {{docs_venv}}/bin/python "git+https://github.com/squidfunk/mike.git"

# Serve the docs locally with live reload at http://127.0.0.1:8000
docs-serve: docs-install
    {{docs_venv}}/bin/zensical serve -f {{docs_config}}

# Serve the docs locally with live reload at http://0.0.0.0:8000
docs-serve-all: docs-install
    {{docs_venv}}/bin/zensical serve -f {{docs_config}} -a 0.0.0.0:8000
    
# Build the static site into docs/site
docs-build: docs-install
    {{docs_venv}}/bin/zensical build -f {{docs_config}}

# Build and serve one version (e.g. "0.1" or "dev") locally through mike, the
# way it will actually be served once deployed to gh-pages
# (mike shells out to `zensical`, so the venv must be on PATH)
docs-serve-version VERSION ALIAS="": docs-install
    PATH="{{justfile_directory()}}/{{docs_venv}}/bin:$PATH" {{docs_venv}}/bin/mike deploy -F {{docs_config}} {{VERSION}} {{ALIAS}}
    PATH="{{justfile_directory()}}/{{docs_venv}}/bin:$PATH" {{docs_venv}}/bin/mike serve -F {{docs_config}}

# Deploy VERSION (aliased ALIAS, e.g. "latest") to the gh-pages branch and push it
docs-deploy VERSION ALIAS="latest": docs-install
    PATH="{{justfile_directory()}}/{{docs_venv}}/bin:$PATH" {{docs_venv}}/bin/mike deploy -F {{docs_config}} --push --update-aliases {{VERSION}} {{ALIAS}}
