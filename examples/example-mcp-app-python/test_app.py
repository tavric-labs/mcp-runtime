"""Offline checks for the sample. Run: python -m pytest test_app.py (needs `mcp<2`)."""
import asyncio

from mcp.shared.memory import create_connected_server_and_client_session

import app


def _run(coro):
    return asyncio.run(coro)


def test_tool_advertises_its_ui_resource():
    async def go():
        async with create_connected_server_and_client_session(app.mcp._mcp_server) as client:
            return {t.name: t for t in (await client.list_tools()).tools}
    tools = _run(go())
    assert tools["show_dashboard"].meta == {"ui": {"resourceUri": app.UI_URI}}
    assert tools["refresh_metrics"].meta is None
    assert "reset_counters" in tools


def test_ui_resource_has_the_app_mime_type_and_csp():
    async def go():
        async with create_connected_server_and_client_session(app.mcp._mcp_server) as client:
            listed = {str(r.uri): r for r in (await client.list_resources()).resources}
            read = await client.read_resource(app.UI_URI)
            return listed, read
    listed, read = _run(go())
    assert listed[app.UI_URI].mimeType == app.APP_MIME
    assert listed[app.UI_URI].meta["ui"]["csp"] == {"connectDomains": []}
    assert "<h1>Metrics</h1>" in read.contents[0].text


def test_skill_file_is_a_resource_with_frontmatter():
    async def go():
        async with create_connected_server_and_client_session(app.mcp._mcp_server) as client:
            return await client.read_resource("skill://metrics-summary/SKILL.md")
    text = _run(go()).contents[0].text
    assert text.startswith("---\nname: metrics-summary\n")
