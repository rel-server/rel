
# Create pre-owned directories
FROM busybox AS setup
RUN mkdir -p /app/secrets /app/saml && \
    chown -R 1000:1000 /app

# FROM alpine:3.12
FROM scratch
LABEL maintainer="Christophe Eymard <christophe.eymard@iqvia.com>"

EXPOSE 3001

# Copy pre-owned directories from the setup stage
COPY --from=setup /app /app

ADD server /server/server
ADD ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

WORKDIR /app

USER 1000:1000

# These are volumes that are to be persisted
VOLUME ["/app/saml", "/app/secrets"]
ENTRYPOINT ["/server/server"]
CMD []
