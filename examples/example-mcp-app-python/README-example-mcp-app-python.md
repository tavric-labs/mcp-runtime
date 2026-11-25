# Sample MCP App and skill (Python)

A small server for trying MCP Apps and a skill through MCP Runtime. It uses the
Python `mcp` SDK (`mcp[cli]<2`), which supports `_meta` on tools and resources.

| Item | What it is |
|---|---|
| `show_dashboard` tool | Carries `_meta.ui.resourceUri = ui://dashboard/main.html`, so a compatible host renders the dashboard |
| `ui://dashboard/main.html` | The app's HTML, served with MIME type `text/html;profile=mcp-app` and `_meta.ui.csp` |
| `refresh_metrics` tool | Called from the dashboard. The host sends it as an ordinary `tools/call` |
| `reset_counters` tool | A write tool the sample grant does not allow, to show a denial |
| `skill://metrics-summary/SKILL.md` | A skill file served as a resource (frontmatter included) |

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

## Not covered

- No server-side `skills/list` or `skills/get`: the SDK has no handler for the Skills
  extension yet, so the skill is only readable with `resources/read`.
- Rendering was not verified in an external MCP Apps host. See
  [#977](https://github.com/tavric-labs/mcp-runtime/issues/977).
