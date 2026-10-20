# syntax=docker/dockerfile:1
FROM golang:1.27.3-alpine AS build
WORKDIR /src
# Download dependencies in a separate layer so source-only changes reuse it.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# modernc SQLite is pure Go, so the release binary does not need libc or CGO.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/6paster ./cmd/6paster

FROM alpine:3.24
# The service runs unprivileged; /var/lib/6paster remains writable for SQLite.
RUN apk add --no-cache ca-certificates \
    && addgroup -g 65532 -S paster \
    && adduser -u 65532 -S -G paster -h /var/lib/6paster paster \
    && mkdir -p /etc/6paster /var/lib/6paster \
    && chown -R paster:paster /var/lib/6paster
COPY --from=build /out/6paster /usr/local/bin/6paster
USER 65532:65532
# EXPOSE is metadata only. Production should publish only the reverse proxy.
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/health || exit 1
CMD ["6paster", "serve"]
