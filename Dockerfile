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
# start out root-owned, and rel (running as 65532) couldn't write into it.
RUN mkdir -p /vol/static /vol/template /vol/dmut /vol/wellknown /vol/data && \
    chown -R 65532:65532 /vol

FROM scratch

ARG VERSION=dev
LABEL org.opencontainers.image.title="rel" \
    org.opencontainers.image.version="${VERSION}" \
    org.opencontainers.image.source="https://github.com/ceymard/rel"

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/rel /rel
COPY --chown=65532:65532 --from=build /vol/static /static
COPY --chown=65532:65532 --from=build /vol/template /template
COPY --chown=65532:65532 --from=build /vol/dmut /dmut
COPY --chown=65532:65532 --from=build /vol/wellknown /wellknown
COPY --chown=65532:65532 --from=build /vol/data /data

# http.static.path (first entry is also the upload-destination write target)
VOLUME ["/static"]
# http.templates.path
VOLUME ["/template"]
# dmut.path
VOLUME ["/dmut"]
# pg.query.wellknown_path
VOLUME ["/wellknown"]
# generated/persisted jwt.secret (REL_JWT__SECRET below), and anywhere else
# a deployment wants rel to write state
VOLUME ["/data"]

ENV REL_JWT__SECRET="\$FILE\$/data/jwt-secret\$GEN\$32"

EXPOSE 8080

WORKDIR /data
USER 65532:65532

ENTRYPOINT ["/rel"]
