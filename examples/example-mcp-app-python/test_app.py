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


# --- Skills extension, over the real HTTP app -----------------------------------
import contextlib
import hashlib
import json

from starlette.testclient import TestClient

HEADERS = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}


def _rpc(client, method, params=None, rid=1, session=None):
    headers = dict(HEADERS)
    if session:
        headers["Mcp-Session-Id"] = session
    body = {"jsonrpc": "2.0", "id": rid, "method": method}
    if params is not None:
        body["params"] = params
    return client.post("/mcp", headers=headers, content=json.dumps(body))


# The SDK's session manager can start only once per server, so every test shares
# one started client.
_SHARED = TestClient(app.http_app(), base_url="http://127.0.0.1:8088")
_SHARED.__enter__()


@contextlib.contextmanager
def _client():
    yield _SHARED


def test_initialize_declares_the_skills_extension_and_keeps_core_capabilities():
    with _client() as c:
        r = _rpc(c, "initialize", {"protocolVersion": "2025-11-25", "capabilities": {}, "clientInfo": {"name": "t", "version": "1"}})
    caps = r.json()["result"]["capabilities"]
    assert caps["extensions"]["io.modelcontextprotocol/skills"] == {"directoryRead": True}
    assert "tools" in caps and "resources" in caps


def test_skills_list_has_frontmatter_manifest_and_matching_digests():
    with _client() as c:
        result = _rpc(c, "skills/list", {}).json()["result"]
    assert result["resultType"] == "complete"
    assert result["cacheScope"] == "public" and result["ttlMs"] > 0
    (skill,) = result["skills"]
    assert skill["uri"] == "skill://metrics-summary/SKILL.md"
    assert skill["frontmatter"] == {"name": "metrics-summary", "description": "Summarize the dashboard metrics in two sentences."}
    uris = [f["uri"] for f in skill["resources"]]
    assert uris == ["skill://metrics-summary/SKILL.md", "skill://metrics-summary/references/thresholds.md"]
    for entry in skill["resources"]:
        content = app.SKILL_FILES[entry["uri"]][1].encode()
        assert entry["digest"] == "sha256:" + hashlib.sha256(content).hexdigest()
        assert entry["size"] == len(content)


def test_skills_get_returns_the_same_entry_and_rejects_unknown_uris():
    with _client() as c:
        got = _rpc(c, "skills/get", {"uri": "skill://metrics-summary/SKILL.md"}).json()["result"]
        listed = _rpc(c, "skills/list", {}).json()["result"]["skills"][0]
        missing = _rpc(c, "skills/get", {"uri": "skill://nope/SKILL.md"}).json()
        bad = _rpc(c, "skills/get", {}).json()
    assert got["resultType"] == "complete" and got["skill"] == listed
    assert missing["error"]["code"] == -32602
    assert bad["error"]["code"] == -32602


def test_directory_read_lists_direct_children_only():
    with _client() as c:
        sub = _rpc(c, "resources/directory/read", {"uri": "skill://metrics-summary/references"}).json()["result"]
        root = _rpc(c, "resources/directory/read", {"uri": "skill://metrics-summary"}).json()["result"]
        missing = _rpc(c, "resources/directory/read", {"uri": "skill://metrics-summary/nope"}).json()
    assert [r["uri"] for r in sub["resources"]] == ["skill://metrics-summary/references/thresholds.md"]
    assert {r["uri"]: r["mimeType"] for r in root["resources"]} == {
        "skill://metrics-summary/SKILL.md": "text/markdown",
        "skill://metrics-summary/references": "inode/directory",
    }
    assert missing["error"]["code"] == -32602


def test_skill_files_are_readable_with_resources_read_and_other_methods_still_work():
    with _client() as c:
        init = _rpc(c, "initialize", {"protocolVersion": "2025-11-25", "capabilities": {}, "clientInfo": {"name": "t", "version": "1"}})
        sid = init.headers["mcp-session-id"]
        _rpc(c, "notifications/initialized", session=sid)
        tools = _rpc(c, "tools/list", rid=2, session=sid).json()["result"]["tools"]
        read = _rpc(c, "resources/read", {"uri": "skill://metrics-summary/references/thresholds.md"}, rid=3, session=sid).json()
    assert {t["name"] for t in tools} == {"show_dashboard", "refresh_metrics", "reset_counters"}
    assert "5 percent" in read["result"]["contents"][0]["text"]
