# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
RUN apk add --no-cache ca-certificates
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/rel \
    ./cmd/rel

# Pre-created and owned by the numeric non-root UID scratch runs as below —
# an anonymous or bind volume mounted here without this would otherwise
# start out root-owned, and rel (running as 1000) couldn't write into it.
# /secrets/jwt is jwt.secret's default $GEN$ candidate (config.DefaultJwtSecret)
# — pre-creating it here means a container with no volume mounted there still
# writes into its own writable layer instead of falling back to the
# ephemeral, console-logged value (specs/configuration.md ## $GEN$
# multi-path resolution).
RUN mkdir -p /vol/static /vol/template /vol/wellknown /vol/secrets/jwt && \
    chown -R 1000:1000 /vol

FROM scratch

ARG VERSION=dev
LABEL org.opencontainers.image.title="rel" \
    org.opencontainers.image.version="${VERSION}" \
    org.opencontainers.image.source="https://github.com/ceymard/rel"

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/rel /rel
COPY --chown=1000:1000 --from=build /vol/static /static
COPY --chown=1000:1000 --from=build /vol/template /template
COPY --chown=1000:1000 --from=build /vol/wellknown /wellknown
COPY --chown=1000:1000 --from=build /vol/secrets /secrets

# http.static.path (first entry is also the upload-destination write target)
VOLUME ["/static"]
# http.templates.path
VOLUME ["/template"]
# pg.query.wellknown_path
VOLUME ["/wellknown"]
# jwt.secret's default $GEN$ path (config.DefaultJwtSecret), and the future
# home for OpenID/SAML secrets alongside it — no REL_JWT__SECRET override
# needed here, this already matches the compiled-in default.
VOLUME ["/secrets"]

EXPOSE 8080

USER 1000:1000

ENTRYPOINT ["/rel"]
