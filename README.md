# 6paster-go

A small, self-hosted paste service written in Go. It is a functional clone of
[6paster](https://github.com/cottow/6paster): submit UTF-8 text or a supported
image, receive an unlisted URL, and let the paste expire automatically.

The service supports MySQL and SQLite, including in-place adoption of the
original 6paster MySQL table.

For a guided local setup and first paste, see
[GETTING_STARTED.md](GETTING_STARTED.md).

## Compatibility and differences

This is a compatible reimplementation, not a line-for-line port. It preserves
the original browser workflow, curl multipart fields (`content` and `ttl`),
24-character unlisted URLs, default limits, supported text/image formats when
enabled, and existing MySQL paste data.

| Area | Original PHP 6paster | This implementation |
| --- | --- | --- |
| Deployment | PHP, a configured web server, and MySQL | One Go binary with embedded templates and migrations; MySQL or CGO-free SQLite |
| Paste IDs | PHP `rand()` mixed with the MySQL password and SHA-1 | Cryptographically secure random identifiers with unbiased character selection and collision retries |
| Rate limiting | Counts IP hashes stored with paste rows | Transaction-safe sliding windows independent of paste expiry; trusted-proxy-aware client addresses |
| Client data | Keeps an IP hash for the lifetime of every paste | Paste rows contain no client identifier; rate-limit hashes expire after at most 24 hours |
| Expiration | Deletes expired rows after handling a request | Rejects expired pastes immediately and cleans them at startup and on a schedule |
| Configuration | PHP settings checked while handling requests | Strict YAML validation before the server starts, including database, URL, TTL, and proxy settings |
| HTTP hardening | Direct-HTTPS check and limited response hardening | Proxy-aware HTTPS enforcement, HSTS on secure requests, no-store caching, CSP, referrer policy, request timeouts, and panic recovery |
| Operations | Manual MySQL schema import | Versioned, repeatable migrations, graceful shutdown, structured logs, containers, and automated tests |

Two small behaviors intentionally differ. An out-of-range `ttl` is rejected
instead of silently clamped, making client mistakes visible. The original
`?smart` PHP syntax-highlighting mode is not implemented; paste responses remain
raw text or configured image content, and the parameter is safely ignored.

## Features

- Browser form and curl-compatible multipart submissions
- Raw UTF-8 text, with optional GIF, JPEG, PNG, and BMP support
- Cryptographically random 24-character paste identifiers
- Configurable one-hour/day sliding-window throttles
- Configurable TTL and upload-size bounds
- MySQL and CGO-free SQLite storage
- Embedded templates and versioned migrations in one binary
- Configurable URL base path and reverse-proxy HTTPS enforcement

## Build and configure

Go 1.26 or newer is required.

```sh
make build
cp config.example.yaml config.yaml
```

Edit `config.yaml`, then initialize or upgrade the selected database and start
the server:

```sh
make migrate
make run
```

Run `make help` to list the development tasks. Override the defaults when
needed, for example `make run CONFIG=config.staging.yaml` or
`make build BINARY=/tmp/6paster`.

The service deliberately does not migrate during startup. Run `migrate`
whenever deploying a newer binary. At startup, configuration decoding is
strict: unknown keys, invalid durations, bad configured URL paths, and
incomplete database settings prevent the server from starting.

Image submissions are enabled by default for compatibility. Set
`paste.allow_images: false` to accept only UTF-8 text. Existing image pastes
return `404 Not Found` while disabled, but remain stored until their normal
expiry and become readable again if image support is re-enabled.

### SQLite

Set `database.driver` to `sqlite` and provide `database.sqlite.path`. The
process needs write access to that file's directory. SQLite runs with WAL,
foreign keys, a busy timeout, and one database connection, making it suitable
for a single service process.

### MySQL

Create an empty database and user, then configure the `database.mysql` block.
Supported `tls_mode` values are `disabled`, `preferred`, `required`, and
`skip-verify`. Timestamps are stored and queried in UTC.

To adopt an existing PHP 6paster database, point the configuration at it and
run `migrate`. Existing `pastes` rows remain in place. The migration converts
the table to InnoDB, adds indexes, and seeds recent throttling events from
existing records before removing the legacy paste-level IP column. This legacy
reconciliation is part of the initial migration; new databases are created
without that column. Back up the database before any production migration.

## Use

Open the configured public URL, or submit a file with curl:

```sh
curl -s -F 'content=<example.txt' -F ttl=604800 \
  -w '%{redirect_url}\n' -o /dev/null https://paste.example.com/
```

`ttl` is a positive whole number of seconds within the configured range;
out-of-range values are rejected. The form derives its choices from that range,
including applicable presets for one hour, four hours, one day, one week, and
30 days.
The query parameter `?smart` is accepted like any other ignored query parameter
and does not enable syntax highlighting.

## Reverse proxy and URL prefix

TLS is normally terminated by a reverse proxy. With `require_https: true`, a
request is accepted when it uses direct TLS or the first
`X-Forwarded-Proto` value is `https`. HSTS is emitted only for such requests.

`X-Forwarded-Proto` is a proxy assertion, so the application listener must not
be reachable by public clients. The proxy must replace any client-supplied
`X-Forwarded-Proto` with the scheme it observed. With that boundary, clients
cannot use the header to bypass `require_https`.

The application limits successful submissions per client over configurable
sliding one-hour and 24-hour windows. It uses the socket peer as the client
address unless that peer belongs to a configured `trusted_proxies` network:

```yaml
server:
  trusted_proxies:
    - 127.0.0.1/32
    - ::1/128
```

Forwarding headers from other peers are ignored. For a trusted peer,
`X-Real-IP` takes precedence; otherwise the service walks `X-Forwarded-For`
from right to left, skipping trusted proxy addresses. The public edge must
replace `X-Real-IP` or append to `X-Forwarded-For`, and it must be the only way
to reach the application listener. Any optional frontend rate limit should be
additional to, not a replacement for, the built-in submission quotas.

For a prefixed deployment, set (for example):

```yaml
server:
  public_url: https://example.com
  base_path: /tools/paste
```

Configure the proxy to forward that prefix unchanged. The form action,
redirect locations, and command-line hint will all include it.

## Containers

See [DOCKER.md](DOCKER.md) for image builds, local SQLite and MySQL Compose
profiles, persistent storage, configuration mounts, migrations, and production
reverse-proxy networking.

## Operations and security

- Paste size is measured in bytes. The default maximum is 300 KiB.
- Expired pastes are inaccessible immediately and removed at startup and once
  per cleanup interval.
- All responses use `Cache-Control: no-store`; fronting proxies and CDNs must
  preserve that directive so cached copies cannot outlive a paste's TTL.
- Successful submission events and their SHA-256 client identifiers are kept
  in dedicated throttling tables for at most 24 hours. Paste rows contain no
  client identifier.
- Paste URLs are unlisted, not authenticated. Anyone with a URL can read it.
- There is intentionally no CSRF check because cross-origin posting is a
  supported use case.

Logs are emitted as structured `slog` records. `SIGINT` and `SIGTERM` trigger a
graceful HTTP shutdown.

## Tests

```sh
go test ./...
go test -race ./...
```

SQLite integration tests run automatically. The MySQL migration/contract test
runs when `TEST_MYSQL_DSN` points to a **disposable** database; it drops and
recreates 6paster tables.

```sh
TEST_MYSQL_DSN='root:root@tcp(127.0.0.1:3306)/paster' go test ./internal/storage
```

## License

BSD 2-Clause. See [LICENSE](LICENSE).
