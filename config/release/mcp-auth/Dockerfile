# Single source of truth for the bundled MCP Auth image that
# `mcp-runtime setup --with-mcp-auth-server` installs by default.
#
# This file is never built. It exists so Dependabot's docker ecosystem can open
# a PR that bumps the tag, and so the CLI can embed the value
# (config/release/mcp-auth/assets.go). Keep exactly one FROM line, with a
# docker.io/princekrroshan01/mcp-auth-server:<MAJOR.MINOR.PATCH> reference.
# The USER line only satisfies the Trivy Dockerfile misconfiguration scan (the
# file is not an image). See docs/contributor/mcp-auth-pin.md.
FROM docker.io/princekrroshan01/mcp-auth-server:0.9.1
USER 65532:65532
