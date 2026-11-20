# Install and configure

6paster-go runs as a single Go binary and stores pastes in MySQL or SQLite.
Go 1.27.3 or newer is required to build from source. For container-based
deployment, see [DOCKER.md](DOCKER.md).

## Build and configure

Build the binary and create a configuration file:

```sh
make build
cp config.example.yaml config.yaml
```

Edit `config.yaml` for your public URL and database. Then initialize or upgrade
the database and start the service:

```sh
./6paster migrate --config config.yaml
./6paster serve --config config.yaml
```

The service deliberately does not migrate during startup. Run `migrate` when
deploying a newer binary. Configuration is validated before the server starts;
unknown keys, invalid durations, bad URL paths, and incomplete database settings
are rejected.

Image submissions are enabled by default. Set `paste.allow_images: false` to
accept only UTF-8 text. Existing image pastes return `404 Not Found` while image
support is disabled, and become readable again if it is re-enabled.

## Database options

For SQLite, set `database.driver` to `sqlite` and configure
`database.sqlite.path`. The process needs write access to the database
directory. SQLite uses WAL, foreign keys, a busy timeout, and one database
connection, so it is intended for a single service process.

For MySQL, create a database and user, then configure the `database.mysql`
block. Supported `tls_mode` values are `disabled`, `preferred`, `required`, and
`skip-verify`. Timestamps are stored and queried in UTC.

To adopt an existing PHP 6paster database, point the configuration at it and
run `migrate`. Existing paste rows remain in place. The initial migration
converts the table to InnoDB, adds indexes, seeds recent throttling events from
existing rows, and removes the legacy paste-level IP column. Back up the
database before a production migration.

## Use the service

Open the configured public URL, or submit a file with curl:

```sh
curl -s -F 'content=<example.txt' -F ttl=604800 \
  -w '%{redirect_url}\n' -o /dev/null https://paste.example.com/
```

`ttl` is a positive whole number of seconds within the configured range;
out-of-range values are rejected. The form offers applicable presets for one
hour, four hours, one day, one week, and 30 days.

[6p-go](https://github.com/hextetsystems/6p-go) is a companion CLI client with
prebuilt releases for Windows, macOS, and Linux. It supports `6p example.txt`
and `echo "hello" | 6p`; see its repository for installation and configuration.

## Reverse proxies and URL prefixes

TLS is normally terminated by a reverse proxy. With `require_https: true`, a
request is accepted when it uses direct TLS or the first `X-Forwarded-Proto`
value is `https`. HSTS is emitted only for such requests.

Forwarding headers are trusted only when the connecting peer is in
`server.trusted_proxies`. For a trusted peer, `X-Real-IP` takes precedence;
otherwise the service walks `X-Forwarded-For` from right to left, skipping
trusted proxies. The proxy must replace the forwarded scheme and client address,
and the application listener must not be reachable by public clients.

For a prefixed deployment, set `server.public_url` and `server.base_path`, for
example:

```yaml
server:
  public_url: https://example.com
  base_path: /tools/paste
```

Configure the proxy to forward the prefix unchanged. The form, redirects, and
CLI hint will include it. Host-level Apache, Caddy, and Nginx examples and
additional deployment guidance are in [DOCKER.md](DOCKER.md).

## Operations and security

- Paste size is measured in bytes; the default maximum is 300 KiB.
- Expired pastes are inaccessible immediately and removed at startup and on a
  regular cleanup schedule.
- Responses use `Cache-Control: no-store`; proxies and CDNs must preserve it.
- Submission rate-limit records are kept for at most 24 hours. Paste rows do not
  contain client identifiers.
- `/metrics` exposes Prometheus counters and duration histograms. Restrict it to
  trusted monitoring clients at the reverse proxy.
- Logs use structured `slog` records. `SIGINT` and `SIGTERM` trigger graceful
  shutdown.

