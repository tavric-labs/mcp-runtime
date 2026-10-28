import { expect, test, type Page } from "@playwright/test";

// Browser fixtures verify UX and request shape; they do not prove live authorization or Tempo contents.
const TRACE = "4bf92f3577b34da6a3ce929d0e0e4736";
const OTHER = "00f067aa0ba902b700f067aa0ba902b7";

async function fixture(page: Page) {
  const context = page.context();
  const requests: string[] = [];
  await page.route("**/config.js", route => route.fulfill({ contentType: "application/javascript", body: "" }));
  await page.route("**/auth/status", route => route.fulfill({ json: { authenticated: true, principal: { role: "admin", subject: "qa-admin" } } }));
  await page.route("**/api/ui/v1/**", route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/ui/v1", "");
    requests.push(path + url.search);
    const wanted = url.searchParams.get("trace_id");
    if (path === "/admin/operations") {
      const all = [
        { created_at: "2026-10-28T10:00:00Z", action: "grant.create", resource: "g-1", status: "success", source: "cli", auth_identity: "alice", trace_id: TRACE, request_id: "req-1" },
        { created_at: "2026-10-28T09:00:00Z", action: "login", resource: "u-1", status: "success", source: "ui", trace_id: OTHER },
        { created_at: "2026-10-27T09:00:00Z", action: "legacy.op", resource: "old", status: "success" },
      ];
      return route.fulfill({ json: { users: [], images: [], audit_logs: wanted ? all.filter(entry => entry.trace_id === wanted) : all } });
    }
    if (path === "/events") {
      const all = [{ timestamp: "2026-10-28T10:00:05Z", namespace: "team-a", server: "files", tool_name: "read", decision: "allow", trace_id: TRACE }];
      return route.fulfill({ json: { events: wanted ? all.filter(event => event.trace_id === wanted) : all } });
    }
    return route.fulfill({ json: {} });
  });
  // Stand in for Grafana so the test can see where the link lands.
  await context.route("**/grafana/explore**", route => route.fulfill({ contentType: "text/html", body: "<title>grafana</title>" }));
  return requests;
}

test("audit trail entries link to their trace in Grafana", async ({ page, context }) => {
  await fixture(page);
  await page.goto("/#/admin/operations");
  await page.getByRole("tab", { name: /Audit trail/ }).click();
  const table = page.getByTestId("operations-audit-table");
  await expect(table.getByTestId("audit-trace-link")).toHaveCount(2); // the legacy row has no trace
  const [popup] = await Promise.all([context.waitForEvent("page"), table.getByTestId("audit-trace-link").first().click()]);
  await popup.waitForURL(/\/grafana\/explore/);
  const url = new URL(popup.url());
  expect(url.pathname).toBe("/grafana/explore");
  const left = JSON.parse(url.searchParams.get("left") || "{}");
  expect(left.datasource).toBe("tempo");
  expect(left.queries[0].query).toBe(TRACE);
  await popup.close();

  await table.getByTestId("audit-inspect").first().click();
  await expect(page.getByTestId("audit-detail")).toContainText(TRACE);
  await expect(page.getByTestId("audit-detail")).toContainText("req-1");
});

test("request traces page merges tool calls and operations and follows one trace", async ({ page }) => {
  const requests = await fixture(page);
  await page.goto("/#/admin/traces");
  const table = page.getByTestId("traces-table");
  await expect(table).toContainText("grant.create");
  await expect(table).toContainText("read");
  await expect(table).toContainText("Tool call");
  await expect(table).toContainText("CLI");
  await table.getByTestId("trace-follow").first().click();
  await expect(page.getByTestId("traces-trace-filter")).toHaveValue(/^[0-9a-f]{32}$/);
  await expect.poll(() => requests.some(request => request.startsWith("/admin/operations") && request.includes("trace_id="))).toBe(true);
  await expect.poll(() => requests.some(request => request.startsWith("/events") && request.includes("trace_id="))).toBe(true);
  await expect(table.getByTestId("trace-follow")).toHaveCount(2); // the tool call and the grant share one trace
});
