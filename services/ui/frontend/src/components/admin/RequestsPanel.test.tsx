import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppProviders } from "../../providers/AppProviders";
import { RequestsPanel } from "./RequestsPanel";

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

const TRACE = "4bf92f3577b34da6a3ce929d0e0e4736";

function stubApi() {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    let json: unknown = {};
    if (url.includes("/analytics/usage")) json = { totals: { events: 42, allowed: 40, denied: 2 }, requests: [], series: [] };
    else if (url.includes("/events")) json = { events: [{ timestamp: "2026-10-28T10:00:05Z", namespace: "team-a", server: "files", tool_name: "read", decision: "allow", trace_id: TRACE }] };
    else if (url.includes("/admin/operations")) json = { users: [], images: [], audit_logs: [{ action: "grant.create", resource: "g-1", status: "success", source: "cli", trace_id: TRACE, created_at: "2026-10-28T10:00:00Z" }] };
    return { ok: true, status: 200, text: async () => "", json: async () => json };
  }));
}

describe("RequestsPanel", () => {
  it("shows usage and traced requests together under one heading with expandable sections", async () => {
    stubApi();
    render(<AppProviders><RequestsPanel onSignIn={() => {}} /></AppProviders>);
    expect(screen.getByRole("heading", { level: 1, name: "Requests" })).toBeInTheDocument();

    expect(await screen.findByTestId("analytics-stats")).toHaveTextContent("42");
    const table = await screen.findByTestId("traces-table");
    expect(await within(table).findByText("grant.create")).toBeInTheDocument();
    expect(within(table).getByText("read")).toBeInTheDocument();

    expect(screen.getByTestId("requests-section-summary")).toHaveAttribute("open");
    expect(screen.getByTestId("requests-section-recent")).toHaveAttribute("open");
    expect(screen.getByTestId("requests-section-decisions")).not.toHaveAttribute("open");
    expect(screen.getByRole("button", { name: "Load gateway decisions" })).toBeInTheDocument();
  });

  it("collapses a section on its summary without unmounting the others", async () => {
    stubApi();
    render(<AppProviders><RequestsPanel onSignIn={() => {}} /></AppProviders>);
    await screen.findByTestId("traces-table");
    const summary = within(screen.getByTestId("requests-section-summary")).getByText("Summary");
    await userEvent.click(summary);
    expect(screen.getByTestId("requests-section-summary")).not.toHaveAttribute("open");
    expect(screen.getByTestId("requests-section-recent")).toHaveAttribute("open");
    expect(screen.getByTestId("traces-table")).toBeInTheDocument();
  });
});
