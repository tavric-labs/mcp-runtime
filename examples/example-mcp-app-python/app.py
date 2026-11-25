"""Sample MCP App server for MCP Runtime.

One tool opens a dashboard UI (an MCP App), one tool is callable from that UI,
and one write tool exists so the sample grant can show a denial. A skill is served
with the Skills extension: files over resources/read, plus skills/list, skills/get
and resources/directory/read answered by a small ASGI wrapper (the SDK has no hook
for them), and the extension declared in the initialize result.
"""
import hashlib
import json
import os
from datetime import datetime, timezone

from mcp.server.fastmcp import FastMCP
from mcp.server.transport_security import TransportSecuritySettings

UI_URI = "ui://dashboard/main.html"
APP_MIME = "text/html;profile=mcp-app"

mcp = FastMCP(
    "example-mcp-app-python",
    instructions="Sample MCP App: a metrics dashboard UI plus a skill.",
    host=os.environ.get("HOST", "0.0.0.0"),
    port=int(os.environ.get("PORT", "8088")),
    json_response=True,
    transport_security=TransportSecuritySettings(enable_dns_rebinding_protection=False),
)

_counters = {"requests": 0, "errors": 0}

DASHBOARD_HTML = """<!doctype html>
<html><head><meta charset="utf-8"><title>Metrics</title>
<style>body{font:14px system-ui;margin:16px}pre{background:#f4f4f5;padding:8px;border-radius:4px}</style></head>
<body>
  <h1>Metrics</h1>
  <pre id="out">connecting...</pre>
  <button id="refresh" disabled>Refresh</button>
  <script>
    // The MCP Apps view protocol: JSON-RPC 2.0 over postMessage with the host.
    // ui/initialize is the only handshake; after ui/notifications/initialized the
    // host sends ui/notifications/tool-result, and tools/call is relayed by the
    // host to the server, where the gateway checks it like any tool call.
    let nextId = 1;
    const pending = new Map();
    const out = document.getElementById('out');
    const button = document.getElementById('refresh');
    const post = (msg) => window.parent.postMessage({jsonrpc: '2.0', ...msg}, '*');
    const request = (method, params) => new Promise((resolve, reject) => {
      const id = nextId++;
      pending.set(id, {resolve, reject});
      post({id, method, params});
    });
    const show = (result) => {
      const text = (result.content || []).map((c) => c.text).join('\n');
      out.textContent = result.isError ? 'error: ' + text : text;
    };
    window.addEventListener('message', (event) => {
      if (event.source !== window.parent) return;
      const msg = event.data;
      if (!msg || msg.jsonrpc !== '2.0') return;
      if (msg.id !== undefined && !msg.method) {
        const waiter = pending.get(msg.id);
        if (!waiter) return;
        pending.delete(msg.id);
        msg.error ? waiter.reject(new Error(msg.error.message)) : waiter.resolve(msg.result);
      } else if (msg.method === 'ui/notifications/tool-result') {
        show(msg.params || {});
      } else if (msg.id !== undefined) {
        post({id: msg.id, result: {}});  // answer host requests we do not handle (ping, teardown)
      }
    });
    button.onclick = async () => {
      try { show(await request('tools/call', {name: 'refresh_metrics', arguments: {}})); }
      catch (err) { out.textContent = 'error: ' + err.message; }
    };
    (async () => {
      await request('ui/initialize', {
        appCapabilities: {}, appInfo: {name: 'Metrics', version: '1.0.0'}, protocolVersion: '2026-01-26',
      });
      post({method: 'ui/notifications/initialized'});
      button.disabled = false;
      out.textContent = 'ready';
    })().catch((err) => { out.textContent = 'init failed: ' + err.message; });
  </script>
</body></html>
"""

SKILL_ROOT = "skill://metrics-summary"
SKILL_FILES = {
    f"{SKILL_ROOT}/SKILL.md": ("text/markdown", """---
name: metrics-summary
description: Summarize the dashboard metrics in two sentences.
---

# Metrics summary

Call `refresh_metrics`, then report the request and error counts. Use
`references/thresholds.md` to decide whether the error rate is a problem.
"""),
    f"{SKILL_ROOT}/references/thresholds.md": ("text/markdown", """# Thresholds

An error rate above 5 percent is a problem; report it first.
"""),
}
SKILL_MD = SKILL_FILES[f"{SKILL_ROOT}/SKILL.md"][1]
SKILLS_EXTENSION = "io.modelcontextprotocol/skills"
SKILL_TTL_MS = 300_000


@mcp.tool(meta={"ui": {"resourceUri": UI_URI}})
def show_dashboard() -> str:
    """Open the metrics dashboard."""
    return "Dashboard opened."


@mcp.tool()
def refresh_metrics() -> dict:
    """Return the current counters. The dashboard UI calls this tool."""
    _counters["requests"] += 1
    return {**_counters, "at": datetime.now(timezone.utc).isoformat()}


@mcp.tool()
def reset_counters() -> str:
    """Reset the counters. A write tool; the sample grant does not allow it."""
    _counters.update(requests=0, errors=0)
    return "reset"


@mcp.resource(
    UI_URI,
    name="dashboard",
    mime_type=APP_MIME,
    meta={"ui": {"csp": {"connectDomains": []}, "permissions": {}}},
)
def dashboard() -> str:
    return DASHBOARD_HTML


def _register_skill_file(uri: str, mime: str, text: str) -> None:
    mcp.resource(uri, name=uri.rsplit("/", 1)[-1], mime_type=mime)(lambda: text)


for _uri, (_mime, _text) in SKILL_FILES.items():
    _register_skill_file(_uri, _mime, _text)


def _frontmatter(text: str) -> dict:
    lines = text.split("\n")
    out = {}
    for line in lines[1 : lines.index("---", 1)]:
        key, _, value = line.partition(":")
        out[key.strip()] = value.strip()
    return out


def _skill_entry() -> dict:
    manifest = []
    for uri, (_mime, text) in SKILL_FILES.items():
        raw = text.encode()
        manifest.append({"uri": uri, "digest": "sha256:" + hashlib.sha256(raw).hexdigest(), "size": len(raw)})
    return {"uri": f"{SKILL_ROOT}/SKILL.md", "frontmatter": _frontmatter(SKILL_MD), "resources": manifest}


def _complete(result: dict) -> dict:
    return {"resultType": "complete", **result, "ttlMs": SKILL_TTL_MS, "cacheScope": "public"}


def _invalid_params(message: str) -> dict:
    return {"code": -32602, "message": message}


def _directory(uri: str) -> list | None:
    """Direct children of a skill directory, or None when it is not one."""
    prefix = uri.rstrip("/") + "/"
    children: dict = {}
    for file_uri, (mime, _text) in SKILL_FILES.items():
        if not file_uri.startswith(prefix):
            continue
        head, sep, _rest = file_uri[len(prefix):].partition("/")
        if sep:
            children[prefix + head] = {"uri": prefix + head, "name": head, "mimeType": "inode/directory"}
        else:
            children[file_uri] = {"uri": file_uri, "name": head, "mimeType": mime}
    return list(children.values()) if children else None


def handle_skills_rpc(rpc: dict) -> dict:
    """Answer skills/list, skills/get and resources/directory/read."""
    params = rpc.get("params") if isinstance(rpc.get("params"), dict) else {}
    method, rid = rpc["method"], rpc.get("id")
    if method == "skills/list":
        result = _complete({"skills": [_skill_entry()]})
    elif method == "skills/get":
        entry = _skill_entry()
        if params.get("uri") != entry["uri"]:
            return {"jsonrpc": "2.0", "id": rid, "error": _invalid_params("unknown skill")}
        result = _complete({"skill": entry})
    else:
        children = _directory(str(params.get("uri", "")))
        if children is None:
            return {"jsonrpc": "2.0", "id": rid, "error": _invalid_params("not a directory")}
        result = _complete({"resources": children})
    return {"jsonrpc": "2.0", "id": rid, "result": result}


_SKILL_METHODS = {"skills/list", "skills/get", "resources/directory/read"}


class SkillsMiddleware:
    """Serves the Skills methods and declares the extension in initialize.

    Everything else is passed to the SDK app untouched.
    """

    def __init__(self, inner):
        self.inner = inner

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http" or scope["method"] != "POST" or scope["path"] != "/mcp":
            return await self.inner(scope, receive, send)
        body = b""
        while True:
            message = await receive()
            body += message.get("body", b"")
            if not message.get("more_body"):
                break
        try:
            rpc = json.loads(body)
        except ValueError:
            rpc = None
        method = rpc.get("method") if isinstance(rpc, dict) else None
        if method in _SKILL_METHODS:
            return await self._reply(send, 200, json.dumps(handle_skills_rpc(rpc)).encode(), [])

        sent = False

        async def replay():
            nonlocal sent
            if not sent:
                sent = True
                return {"type": "http.request", "body": body, "more_body": False}
            return await receive()

        if method == "initialize":
            return await self._initialize(scope, replay, send)
        await self.inner(scope, replay, send)

    @staticmethod
    async def _reply(send, status, payload, headers):
        headers = [(k, v) for k, v in headers if k.lower() != b"content-length"]
        headers += [(b"content-type", b"application/json"), (b"content-length", str(len(payload)).encode())]
        await send({"type": "http.response.start", "status": status, "headers": headers})
        await send({"type": "http.response.body", "body": payload})

    async def _initialize(self, scope, receive, send):
        start, chunks = None, []

        async def capture(message):
            nonlocal start
            if message["type"] == "http.response.start":
                start = message
            elif message["type"] == "http.response.body":
                chunks.append(message.get("body", b""))

        await self.inner(scope, receive, capture)
        payload = b"".join(chunks)
        try:
            doc = json.loads(payload)
            doc["result"]["capabilities"].setdefault("extensions", {})[SKILLS_EXTENSION] = {"directoryRead": True}
            payload = json.dumps(doc).encode()
        except (ValueError, KeyError, TypeError, AttributeError):
            pass  # not a JSON initialize result: pass it through unchanged
        await self._reply(send, start["status"], payload, start["headers"])


def http_app():
    app = mcp.streamable_http_app()
    origins = [o.strip() for o in os.environ.get("CORS_ALLOW_ORIGINS", "").split(",") if o.strip()]
    if origins:
        # Browser-based MCP hosts call the server directly; allow only listed origins.
        from starlette.middleware.cors import CORSMiddleware

        app = CORSMiddleware(
            app, allow_origins=origins, allow_methods=["GET", "POST", "DELETE", "OPTIONS"],
            allow_headers=["*"], expose_headers=["Mcp-Session-Id"],
        )
    return SkillsMiddleware(app)


if __name__ == "__main__":
    import uvicorn

    uvicorn.run(http_app(), host=mcp.settings.host, port=mcp.settings.port)
