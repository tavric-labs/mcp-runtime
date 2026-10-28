import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppProviders } from "../../providers/AppProviders";
import { OperationsPanel } from "./OperationsPanel";
import { traceExploreURL } from "./UsageAnalyticsPanel";

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

const TRACE = "4bf92f3577b34da6a3ce929d0e0e4736";

describe("OperationsPanel audit trail", () => {
  it("links each traced entry to its trace in Grafana and shows a dash when none was recorded", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({
      ok: true, status: 200, text: async () => "",
      json: async () => ({
        users: [], images: [],
        audit_logs: [
          { action: "grant.create", resource: "g-1", status: "success", trace_id: TRACE, request_id: "req-1", created_at: "2026-10-28T10:00:00Z" },
          { action: "login", resource: "u-1", status: "success", created_at: "2026-10-28T09:00:00Z" },
        ],
      }),
    })));
    render(<AppProviders><OperationsPanel onSignIn={() => {}} /></AppProviders>);
    await userEvent.click(await screen.findByRole("tab", { name: /Audit trail/ }));
    const table = await screen.findByTestId("operations-audit-table");
    const links = within(table).getAllByTestId("audit-trace-link");
    expect(links).toHaveLength(1);
    expect(links[0]).toHaveAttribute("href", traceExploreURL(TRACE));
    expect(links[0]).toHaveAttribute("target", "_blank");

    await userEvent.click(within(table).getAllByTestId("audit-inspect")[0]);
    const detail = await screen.findByTestId("audit-detail");
    expect(within(detail).getByText(TRACE)).toBeInTheDocument();
    expect(within(detail).getByText("req-1")).toBeInTheDocument();
  });
});
