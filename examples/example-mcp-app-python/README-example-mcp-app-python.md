# Sample MCP App and skill (Python)

A small server for trying MCP Apps and a skill through MCP Runtime. It uses the
Python `mcp` SDK (`mcp[cli]<2`), which supports `_meta` on tools and resources.

| Item | What it is |
|---|---|
| `show_dashboard` tool | Carries `_meta.ui.resourceUri = ui://dashboard/main.html`, so a compatible host renders the dashboard |
| `ui://dashboard/main.html` | The app's HTML, served with MIME type `text/html;profile=mcp-app` and `_meta.ui.csp` |
| `refresh_metrics` tool | Called from the dashboard. The host sends it as an ordinary `tools/call` |
| `reset_counters` tool | A write tool the sample grant does not allow, to show a denial |
| `skill://metrics-summary/SKILL.md` and `references/thresholds.md` | A skill and a supporting file. The server answers `skills/list`, `skills/get` and `resources/directory/read` (with sha256 digests and sizes) and declares `io.modelcontextprotocol/skills` with `directoryRead: true` in the `initialize` result |

## How the gateway treats it

- The dashboard HTML is a resource, so the grant's `primitiveRules` decide whether it
  can be read. The sample grant allows `ui://dashboard/*` and `skill://metrics-summary/*`
  and nothing else.
- A call from the app is a normal `tools/call`. `refresh_metrics` is granted;
  `reset_counters` is denied with `tool_not_granted` even though it comes from the app.
- Content-Security-Policy and permissions in `_meta.ui` are enforced by the host, not
  the gateway.

## Run it

```bash
python -m pytest test_app.py          # offline checks, needs `pip install "mcp[cli]<2" pytest`
mcp-runtime server validate --grant-file .mcp/grant-example-agent.yaml --session-file .mcp/session-example-agent.yaml
mcp-runtime server build image example-mcp-app-python-gateway   # then: server push, server deploy
cd .mcp
mcp-runtime access grant apply --file grant-example-agent.yaml
mcp-runtime access session apply --file session-example-agent.yaml
```

## How it is built

- The dashboard speaks the MCP Apps view protocol directly (no build step): a
  `ui/initialize` request, then `ui/notifications/initialized`, then it receives
  `ui/notifications/tool-result` and calls tools with `tools/call` over `postMessage`.
  The method names were taken from the `ext-apps` source (protocol `2026-01-26`).
- The Python SDK has no hook for the Skills methods, so `SkillsMiddleware` in `app.py`
  answers them and adds the extension to the `initialize` result; everything else goes
  to the SDK untouched.
- `CORS_ALLOW_ORIGINS` (comma-separated) enables CORS for browser-based MCP hosts. It is
  off by default.

## Not covered

- Rendering was not verified in an external MCP Apps host. The offline tests and an HTTP
  smoke test check the server side; the view protocol is written from the `ext-apps`
  source, not exercised against a real host. See
  [#977](https://github.com/tavric-labs/mcp-runtime/issues/977).
- The Skills methods are served for the `initialize` (2025-11-25) style only. The sample
  does not implement `server/discover` or the stateless `2026-07-28` request style.
- The Skills extension spec is Beta; digests and the manifest follow its published examples.
