import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppProviders } from "../../providers/AppProviders";
import { TracesPanel } from "./TracesPanel";

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

const TRACE = "4bf92f3577b34da6a3ce929d0e0e4736";
const audit = { action: "grant.create", resource: "g-1", status: "success", source: "cli", auth_identity: "alice", trace_id: TRACE, created_at: "2026-10-28T10:00:00Z" };
const toolCall = { timestamp: "2026-10-28T10:00:05Z", namespace: "team-a", server: "files", tool_name: "read", decision: "allow", trace_id: TRACE };

function stubFetch(options: { eventsFail?: boolean } = {}) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/events")) {
      return options.eventsFail
        ? { ok: false, status: 503, json: async () => ({ error: "unavailable" }), text: async () => "" }
        : { ok: true, status: 200, json: async () => ({ events: [toolCall] }), text: async () => "" };
    }
    return { ok: true, status: 200, json: async () => ({ users: [], audit_logs: [audit], images: [] }), text: async () => "" };
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("TracesPanel", () => {
  it("lists platform operations and tool calls together with their trace ids", async () => {
    stubFetch();
    render(<AppProviders><TracesPanel onSignIn={() => {}} /></AppProviders>);
    const table = await screen.findByTestId("traces-table");
    await waitFor(() => expect(within(table).getByText("read")).toBeInTheDocument());
    expect(within(table).getByText("grant.create")).toBeInTheDocument();
    expect(within(table).getByText("CLI")).toBeInTheDocument();
    expect(within(table).getByText("Tool call")).toBeInTheDocument();
    expect(within(table).getAllByText(TRACE.slice(0, 8))).toHaveLength(2);
  });

  it("narrows both sources to one trace when its id is selected", async () => {
    const fetchMock = stubFetch();
    render(<AppProviders><TracesPanel onSignIn={() => {}} /></AppProviders>);
    const table = await screen.findByTestId("traces-table");
    await waitFor(() => expect(within(table).getAllByTestId("trace-follow")).toHaveLength(2));
    await userEvent.click(within(table).getAllByTestId("trace-follow")[0]);
    await waitFor(() => {
      const urls = fetchMock.mock.calls.map((call) => String(call[0]));
      expect(urls.some((url) => url.includes("/admin/operations") && url.includes(`trace_id=${TRACE}`))).toBe(true);
      expect(urls.some((url) => url.includes("/events") && url.includes(`trace_id=${TRACE}`))).toBe(true);
    });
    expect(screen.getByTestId("traces-trace-filter")).toHaveValue(TRACE);
  });

  it("rejects a malformed trace id without querying", async () => {
    const fetchMock = stubFetch();
    render(<AppProviders><TracesPanel onSignIn={() => {}} /></AppProviders>);
    await screen.findByTestId("traces-table");
    const callsBefore = fetchMock.mock.calls.length;
    await userEvent.type(screen.getByTestId("traces-trace-filter"), "not-a-trace");
    await userEvent.click(screen.getByTestId("traces-apply"));
    expect(await screen.findByText(/32 lowercase hex digits/)).toBeInTheDocument();
    expect(fetchMock.mock.calls.length).toBe(callsBefore);
  });

  it("still shows platform operations when the gateway events path is unavailable", async () => {
    stubFetch({ eventsFail: true });
    render(<AppProviders><TracesPanel onSignIn={() => {}} /></AppProviders>);
    expect(await screen.findByTestId("traces-gateway-unavailable")).toBeInTheDocument();
    expect(within(await screen.findByTestId("traces-table")).getByText("grant.create")).toBeInTheDocument();
  });
});
