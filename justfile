db_container := "rel-dev-db"
db_name := "hotel"
db_user := "hotel"
db_password := "test"

# Run the full test suite (testcontainers spins up its own throwaway Postgres
# per package — docker must be running, but `just db-up` is not required)
test:
    go test ./...

# Launch the rel server against the dev database (just db-up first)
run:
    REL_PG__HOST="$(docker inspect {{db_container}} --format '{{"{{"}}.NetworkSettings.Networks.bridge.IPAddress}}')" \
        REL_PG__PORT=5432 \
        REL_PG__QUERIER__USER={{db_user}} REL_PG__QUERIER__PASSWORD={{db_password}} \
        REL_PG__DATABASE={{db_name}} \
        go run ./cmd/rel

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
