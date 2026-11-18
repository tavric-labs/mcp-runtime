# Bundled MCP Auth pin

The default image for `setup --with-mcp-auth-server` is pinned in one file:
[`config/release/mcp-auth/Dockerfile`](https://github.com/tavric-labs/mcp-runtime/blob/main/config/release/mcp-auth/Dockerfile).
It is a single `FROM docker.io/princekrroshan01/mcp-auth-server:<MAJOR.MINOR.PATCH>`
line. The file is never built; it exists so Dependabot's `docker` ecosystem can
update the tag and so the CLI can embed it (`config/release/mcp-auth/assets.go`,
the `--mcp-auth-server-image` default). The default is never `latest`.

Nothing else repeats the number. `setup --help`, the docs and the deployment
profile point at the pin file, and the setup golden stores `mcp-auth-server:<pinned>`.

## How a bump happens

1. When MCP Auth publishes a release, the daily Dependabot run opens a
   `fix(setup): bump ... mcp-auth-server` PR that changes only that one line.
   To trigger it sooner, open **Insights > Dependency graph > Dependabot** in the
   repository, pick the `/config/release/mcp-auth` docker entry and choose
   **Check for updates**.
2. CI runs as for any PR. It checks that the pin parses as
   `docker.io/princekrroshan01/mcp-auth-server:<semver>`, that the setup flag
   default equals the pin, that no doc or profile repeats a version, and the
   setup help golden. A bump needs no other edits.
3. Review MCP Auth's release notes, then add a `CHANGELOG.md` `Unreleased`
   `Changed` entry if the bump changes behavior users or operators see.
   Merging does not roll out anything: existing installs keep their image until
   `mcp-runtime update --only mcp-auth` or the hosted deploy's
   `mcp_auth_version` input (which does not use the default).
4. The [release gate](release-gate.md) still applies. Run the Pre-release
   Regression, which exercises the bundled MCP Auth, before tagging a release
   that carries a new pin.

## Manual fallback

Edit the tag in `config/release/mcp-auth/Dockerfile` and nothing else, then run
`go test ./config/release/... ./internal/cli/setup/... ./test/golden/...`.
