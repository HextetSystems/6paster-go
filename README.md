# 6paster-go

6paster-go is a small, self-hosted paste service written in Go. It accepts UTF-8
text and optional images, returns unlisted URLs, and expires pastes automatically.
It supports MySQL and SQLite and can adopt an existing 6paster MySQL database.

For setup and deployment, see [INSTALL.md](INSTALL.md). For local development,
see [DEVELOPING.md](DEVELOPING.md). Container deployment details are in
[DOCKER.md](DOCKER.md).

The service is a compatible reimplementation of
[6paster](https://github.com/cottow/6paster), with secure random paste IDs,
configurable limits, and privacy-conscious rate limiting. Paste URLs are
unlisted, not authenticated: anyone with a URL can read the paste.

BSD 2-Clause. See [LICENSE](LICENSE).
