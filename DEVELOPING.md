# Development

This guide covers running 6paster-go locally and working on its Go code. Run
commands from the repository root. Go 1.27.3 or newer and curl are required.

## Run locally with SQLite

Create a local configuration:

```sh
cp config.example.yaml config.yaml
```

For local HTTP development, use settings like these in `config.yaml`:

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

The remaining defaults can stay unchanged. Local configuration and SQLite
files are ignored by Git. Initialize the database and start the service:

```sh
make migrate
make run
```

The server listens on `:8080`. Stop it with `Ctrl-C`; shutdown is graceful.
After changing Go code, restart the server. Run `make migrate` after changing
an embedded migration.

## Exercise the service

In another terminal, check the form and create/retrieve a paste:

```sh
curl -i http://localhost:8080/

paste_url=$(curl -sS \
  -F 'content=hello from the development shell' \
  -F ttl=3600 \
  -o /dev/null \
  -w '%{redirect_url}' \
  http://localhost:8080/)

curl -i "$paste_url"
```

For image uploads, use GIF, JPEG, PNG, or BMP files with `paste.allow_images:
true`. Set it to `false` to accept only UTF-8 text. Unsupported binary formats
return `415 Unsupported Media Type`.

## Tests and common tasks

```sh
make test
make test-race
make vet
```

`make check` runs the race tests and vet together. Other useful tasks are
`make build`, `make fmt`, `make deps`, and `make clean`. Run `make help` to see
the available targets. `make clean` removes the service binary and test
artifacts, but leaves configuration and database files intact.

SQLite integration tests use isolated temporary databases. The MySQL
integration test runs only when `TEST_MYSQL_DSN` points to a disposable
database; it drops and recreates service tables:

```sh
TEST_MYSQL_DSN='paster:password@tcp(127.0.0.1:3306)/paster_test' \
  go test ./internal/storage -run TestMySQLLegacyMigrationAndContract -v
```

You can run an individual package or test while iterating:

```sh
go test ./internal/web
go test ./internal/storage -run TestSQLiteRateLimitSurvivesPasteCleanup -v
```

## Try HTTPS enforcement and a URL prefix

To exercise HTTPS enforcement locally, set `require_https: true` and restart.
A plain request should be rejected; a simulated proxy request should succeed:

```sh
curl -i http://localhost:8080/
curl -i -H 'X-Forwarded-Proto: https' http://localhost:8080/
```

The second response includes `Strict-Transport-Security`. Restore
`require_https: false` for ordinary local development.

To test a URL prefix, set:

```yaml
server:
  public_url: "http://localhost:8080"
  base_path: "/tools/paste"
  require_https: false
```

Then try `http://localhost:8080/tools/paste/`; requests to the old root path
return `404 Not Found`.

