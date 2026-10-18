# Docker

The repository includes a multi-stage production `Dockerfile` and a Compose
stack for local SQLite or MySQL use.

## Build the image

```sh
docker build -t 6paster-go .
```

The final Alpine image contains only the statically built application and CA
certificates. It runs as the unprivileged user and group `65532:65532`. The
image declares port 8080 and accepts configuration through `SIXPASTER_*`
environment variables or an optional YAML file.

The default image command starts the server but does not apply migrations.
Apply migrations before starting a newly deployed version. The local Compose
services demonstrate this by running `6paster migrate` before `6paster serve`.
The image health check requests `GET /health` on port 8080 every 30 seconds.
The endpoint is independent of `server.base_path` and HTTPS enforcement so it
can be checked from inside the container without traversing the public proxy.

## Run the published image with Compose

[`compose.ghcr.yaml`](compose.ghcr.yaml) is a standalone Compose file for
deploying the published GHCR image; it does not build from this repository.
Create an environment file and set at least the public URL for your deployment:

```sh
cp .env.example .env
# Edit .env and set SIXPASTER_SERVER_PUBLIC_URL before continuing.
docker compose -f compose.ghcr.yaml pull
docker compose -f compose.ghcr.yaml up -d
```

The example uses SQLite at `/var/lib/6paster/pastes.db` and includes every
supported application setting. The default image is
`ghcr.io/hextetsystems/6paster-go:latest`. Set `PASTER_VERSION` in `.env` to a
specific released version to pin the deployment, or override it for one run:

```sh
PASTER_VERSION=1.2.3 docker compose -f compose.ghcr.yaml up -d
```

The service is intentionally bound only to `127.0.0.1:8080`; put a TLS reverse
proxy in front of it for public access. The `paster-data` named volume persists
the SQLite database and its WAL files across container upgrades.

## Configuration precedence

Configuration is applied in this order: built-in defaults, optional YAML, then
`SIXPASTER_*` environment variables. Environment variables therefore override
matching YAML values. With no `--config` flag, `config.yaml` is loaded when it
exists and otherwise configuration comes from defaults and the environment.
An explicitly requested file must exist.

Advanced Compose users can use YAML by adding this mount to the service:

```yaml
volumes:
  - ./config.yaml:/etc/6paster/config.yaml:ro
```

Then add the explicit path to both commands while retaining `env_file` if
environment overrides are desired:

```yaml
command: ["/bin/sh", "-c", "6paster migrate --config /etc/6paster/config.yaml && exec 6paster serve --config /etc/6paster/config.yaml"]
```

## Published images

Version tags publish a multi-platform image for Linux AMD64 and ARM64 at
`ghcr.io/hextetsystems/6paster-go`. For a `v1.2.3` release, any of these tags can
be pulled:

```sh
docker pull ghcr.io/hextetsystems/6paster-go:1.2.3
docker pull ghcr.io/hextetsystems/6paster-go:1.2
docker pull ghcr.io/hextetsystems/6paster-go:1
docker pull ghcr.io/hextetsystems/6paster-go:latest
```

Use the exact version in production for reproducible deployments. Stable
releases update all four tags. Prerelease tags publish only their exact version
and do not move `latest`.

## Local Compose profiles

Start the SQLite profile:

```sh
docker compose --profile sqlite up --build
```

Or start the application with MySQL:

```sh
docker compose --profile mysql up --build
```

Both profiles publish the service at <http://localhost:8080>, apply migrations
before serving, and persist database data in named volumes. Stop a profile with
`docker compose --profile PROFILE down`. Add `--volumes` only when you also
intend to delete its database data.

The checked-in files under `deploy/` are local examples. They disable HTTPS and
use development-only MySQL credentials; do not reuse those settings in
production.

## Mounts and persistence

Compose short volume syntax is:

```text
SOURCE:DESTINATION[:OPTIONS]
```

`SOURCE` is a host path or Docker-managed named volume. `DESTINATION` is the
path inside the container. For example:

```yaml
volumes:
  - sqlite-data:/var/lib/6paster
  - ./deploy/config.sqlite.yaml:/etc/6paster/config.yaml:ro
```

The first entry persists SQLite data in the named volume `sqlite-data`. The
second mounts a host configuration file at the path expected by the container;
`ro` makes that mount read-only.

For SQLite, persist `/var/lib/6paster` and ensure it is writable by UID/GID
65532. This directory contains the database and its WAL files. For MySQL, the
database container owns persistence; the application container needs only its
configuration file.

## Production networking

Do not publish the application port directly in production. Put 6paster and its
Nginx, Caddy, or Traefik proxy on a private Docker network, omit `ports` from the
application service, and make the proxy the only public ingress. Keep unrelated
or untrusted containers off that network because they could otherwise bypass
the proxy.

Enable `server.require_https` and configure the proxy to replace
`X-Forwarded-Proto` with the scheme it observed. Configure `trusted_proxies`
only for the actual proxy networks so rate limiting receives the correct client
address. See the README's
[reverse-proxy guidance](README.md#reverse-proxy-and-url-prefix) for forwarding
header handling and the built-in submission limits.

A production application service will generally include:

```yaml
services:
  paster:
    image: 6paster-go:latest
    networks:
      - proxy
    volumes:
      - ./config.yaml:/etc/6paster/config.yaml:ro
    command:
      - /bin/sh
      - -c
      - 6paster migrate --config /etc/6paster/config.yaml && exec 6paster serve --config /etc/6paster/config.yaml
    restart: unless-stopped

networks:
  proxy:
    internal: true
```

This is a structural example, not a complete proxy deployment. An `internal`
network prevents external routing from that network; attach the proxy to a
second ingress-capable network as required by its deployment.

## Publishing a release

After the intended commit has passed CI on `main`, create and push an annotated
semantic-version tag:

```sh
git switch main
git pull --ff-only
git tag -a v1.2.3 -m "v1.2.3"
git push origin v1.2.3
```

The tag runs CI again. After it passes, GoReleaser publishes the Linux AMD64
and ARM64 archives, checksum file, and changelog to a GitHub Release, while the
Docker workflow publishes the GHCR image tags described above. Do not move or
reuse a published version tag; create a new version instead.
