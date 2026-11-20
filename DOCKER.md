# Docker

The repository includes a multi-stage production `Dockerfile`, Compose files
for local SQLite/MySQL use, and production examples under `deploy/`. For
application configuration and proxy security, see
[INSTALL.md](INSTALL.md#reverse-proxies-and-url-prefix).

## Build and run locally

Build the image:

```sh
docker build -t 6paster-go .
```

Run a local SQLite stack:

```sh
docker compose --profile sqlite up --build
```

Or use MySQL:

```sh
docker compose --profile mysql up --build
```

Both profiles listen on <http://localhost:8080>, run migrations before serving,
and persist database data in named volumes. Stop a profile with
`docker compose --profile PROFILE down`. Add `--volumes` only to delete its
database data. The sample `deploy/config.*.yaml` files are for local use; the
MySQL sample uses development credentials.

## Production deployment

[`deploy/compose.yaml`](deploy/compose.yaml) runs the published GHCR image with
SQLite persistence and binds the app to `127.0.0.1:8080` for a host-level TLS
proxy:

```sh
cp deploy/.env.example deploy/.env
cd deploy
# Set SIXPASTER_SERVER_PUBLIC_URL in .env.
docker compose pull
docker compose up -d
```

The sample proxy configurations are [`apache.conf`](deploy/apache.conf),
[`nginx.conf`](deploy/nginx.conf), and [`Caddyfile`](deploy/Caddyfile). Replace
`paste.example.com` and configure certificates for Apache or Nginx; Caddy
obtains certificates automatically.

Set `SIXPASTER_SERVER_TRUSTED_PROXIES` to the Compose network gateway CIDR so
the app accepts proxy headers and rate-limits the originating client. Find the
gateway with:

```sh
docker network inspect deploy_default
```

Set the value to `<gateway>/32` in `deploy/.env` and recreate the service with
`docker compose up -d`. The proxy must replace forwarded scheme and client
address headers. Keep the application port private; see
[INSTALL.md](INSTALL.md#reverse-proxies-and-url-prefix).

[`compose.ghcr.yaml`](compose.ghcr.yaml) is a root-level alternative. Create
`.env` from `.env.example`, set `SIXPASTER_SERVER_PUBLIC_URL`, then run:

```sh
docker compose -f compose.ghcr.yaml pull
docker compose -f compose.ghcr.yaml up -d
```

Both production files use SQLite by default. The image accepts `SIXPASTER_*`
environment variables or an optional YAML file; environment variables override
YAML values. It runs as UID/GID `65532`, checks `/health`, and does not migrate
automatically outside the supplied Compose startup commands. Apply migrations
before starting a new version. Persist `/var/lib/6paster` for SQLite and ensure
it is writable by UID/GID 65532.

## Image releases

The published image is `ghcr.io/hextetsystems/6paster-go`, with version tags
for Linux AMD64 and ARM64. Pin an exact version in production; `latest` tracks
the current stable release. Version tags are published from Git tags; see the
repository workflows for release automation.
