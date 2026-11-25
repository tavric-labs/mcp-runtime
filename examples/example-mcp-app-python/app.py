"""Sample MCP App server for MCP Runtime.

One tool opens a dashboard UI (an MCP App), one tool is callable from that UI,
and one write tool exists so the sample grant can show a denial. A skill is
published as ordinary resources, which is how the Skills extension serves files.
"""
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
    transport_security=TransportSecuritySettings(enable_dns_rebinding_protection=False),
)

_counters = {"requests": 0, "errors": 0}

DASHBOARD_HTML = """<!doctype html>
<html><head><meta charset="utf-8"><title>Metrics</title></head>
<body>
  <h1>Metrics</h1>
  <pre id="out">loading...</pre>
  <button id="refresh">Refresh</button>
  <script>
    // A host-provided bridge calls the server's tools. In the host, that call is
    // an ordinary tools/call and is checked against the caller's grant.
    document.getElementById('refresh').onclick = () => window.parent.postMessage(
      {jsonrpc: '2.0', id: 1, method: 'tools/call', params: {name: 'refresh_metrics', arguments: {}}}, '*');
  </script>
</body></html>
"""

SKILL_MD = """---
name: metrics-summary
description: Summarize the dashboard metrics in two sentences.
---

# Metrics summary

Call `refresh_metrics`, then report the request and error counts and whether the
error rate is above 5 percent.
"""


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


@mcp.resource("skill://metrics-summary/SKILL.md", name="metrics-summary-skill", mime_type="text/markdown")
def metrics_summary_skill() -> str:
    return SKILL_MD


if __name__ == "__main__":
    mcp.run(transport="streamable-http")
