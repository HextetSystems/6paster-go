# Getting started

This guide uses SQLite for the fastest local development loop. Terminal 1 runs
the service; Terminal 2 exercises it with curl and runs the automated tests.

## Prerequisites

- Go 1.26 or newer
- curl
- A shell such as Bash

Run all commands from the repository root.

## Terminal 1: configure and run the service

Create a local configuration:

```sh
cp config.example.yaml config.yaml
```

Edit these fields in `config.yaml` for local HTTP development:

```yaml
server:
  listen: ":8080"
  public_url: "http://localhost:8080"
  base_path: "/"
  require_https: false
  page_title: "6paster development"

database:
  driver: "sqlite"
  sqlite:
    path: "./6paster-dev.db"

paste:
  allow_images: true
```

The remaining defaults can stay unchanged. Both `config.yaml` and local SQLite
files are ignored by Git.

Apply the embedded database migrations:

```sh
make migrate
```

Start the server:

```sh
make run
```

The log should report that the service is listening on `:8080`. Leave it
running while using Terminal 2. Stop it with `Ctrl-C`; shutdown is graceful.

After changing Go code, stop and rerun the `serve` command. Run `migrate`
again after changing an embedded migration.

## Terminal 2: exercise the service

Confirm that the browser form is available:

```sh
curl -i http://localhost:8080/
```

Create a text paste and capture its redirect URL:

```sh
paste_url=$(curl -sS \
  -F 'content=hello from the development shell' \
  -F ttl=3600 \
  -o /dev/null \
  -w '%{redirect_url}' \
  http://localhost:8080/)

printf 'Paste URL: %s\n' "$paste_url"
```

Retrieve the paste:

```sh
curl -i "$paste_url"
```

The response should be `200 OK`, have
`Content-Type: text/plain; charset=utf-8`, and contain the submitted text.

Test the original 6paster file-upload convention:

```sh
printf 'content read from a file\n' >/tmp/6paster-example.txt

file_url=$(curl -sS \
  -F 'content=</tmp/6paster-example.txt' \
  -F ttl=604800 \
  -o /dev/null \
  -w '%{redirect_url}' \
  http://localhost:8080/)

curl -i "$file_url"
```

With `paste.allow_images: true`, GIF, JPEG, PNG, and BMP files can be submitted
using the same `content=<file` syntax. Other binary formats return
`415 Unsupported Media Type`. Set `allow_images: false` to accept only UTF-8
text; new image submissions then return `415`, and existing image URLs return
`404` until image support is re-enabled.

Useful negative checks:

```sh
# Missing content: 400 Bad Request
curl -i -F ttl=3600 http://localhost:8080/

# Invalid TTL: 400 Bad Request
curl -i -F content=hello -F ttl=later http://localhost:8080/

# Unknown paste: 404 Not Found
curl -i http://localhost:8080/p/aaaaaaaaaaaaaaaaaaaaaaaa

# Unsupported method: 405 Method Not Allowed
curl -i -X DELETE http://localhost:8080/
```

To inspect the redirect itself rather than using `%{redirect_url}`:

```sh
curl -i -F content=hello -F ttl=3600 http://localhost:8080/
```

A successful submission returns `303 See Other` with a `Location` header.

## Run the automated tests

Terminal 1 can keep serving while these run in Terminal 2; tests use isolated
temporary SQLite databases.

```sh
make test
make test-race
make vet
# Or run the race tests and vet together:
make check
```

Run an individual package or test while iterating:

```sh
go test ./internal/web
go test ./internal/storage -run TestSQLiteRateLimitSurvivesPasteCleanup -v
```

Other common tasks are `make build`, `make fmt`, `make deps`, and `make clean`.
`make clean` removes only the service binary and test artifacts; it does not
delete `config.yaml` or any database.

The MySQL integration test is skipped unless `TEST_MYSQL_DSN` is set. The
database named by this variable must be disposable because the test drops and
recreates the service tables:

```sh
TEST_MYSQL_DSN='paster:password@tcp(127.0.0.1:3306)/paster_test' \
  go test ./internal/storage -run TestMySQLLegacyMigrationAndContract -v
```

## Test HTTPS enforcement locally

Change `require_https` to `true` and restart Terminal 1. A request without the
proxy header should be rejected:

```sh
curl -i http://localhost:8080/
```

Simulate a TLS-terminating reverse proxy by setting its forwarded scheme:

```sh
curl -i -H 'X-Forwarded-Proto: https' http://localhost:8080/
```

The second response should be successful and include
`Strict-Transport-Security`. Restore `require_https: false` for ordinary local
development.

## Test a URL prefix

Set the following values and restart Terminal 1:

```yaml
server:
  public_url: "http://localhost:8080"
  base_path: "/tools/paste"
  require_https: false
```

The form and submission endpoint then move to:

```sh
curl -i http://localhost:8080/tools/paste/
curl -i -F content=hello -F ttl=3600 \
  http://localhost:8080/tools/paste/
```

Requests to the old root path should return `404 Not Found`.

## Optional container workflow

For an equivalent SQLite or MySQL container workflow, see
[DOCKER.md](DOCKER.md). The curl examples above work unchanged with either
local Compose profile.
