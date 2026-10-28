import { expect, test, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";

async function fixture(page: Page, failUsage = false) {
  const queries: URLSearchParams[] = [];
  await page.route("**/config.js", route => route.fulfill({ contentType: "application/javascript", body: "" }));
  await page.route("**/auth/status", route => route.fulfill({ json: { authenticated: true, principal: { role: "admin", subject: "admin" } } }));
  await page.route("**/api/ui/v1/**", route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/ui/v1", "");
    if (path === "/analytics/usage") {
      queries.push(url.searchParams);
      if (failUsage) return route.fulfill({ status: 503, json: { error: "Usage unavailable" } });
      return route.fulfill({ json: {
        totals: { events: 1200, allowed: 1140, denied: 60, unique_servers: 3, unique_humans: 5, unique_agents: 2, unique_sessions: 8, http_failures: 64, server_errors: 4, p95_latency_ms: 142, average_latency_ms: 48, bytes_out: 2048000 },
        bucket_seconds: 3600,
        series: [0, 1, 2, 3].map((i) => ({ bucket: `2026-10-25T0${i}:00:00Z`, events: 300, allowed: 285, denied: 15, requests_per_second: 300 / 3600 })),
        requests: [{ server: "docs-search", namespace: "team-acme", team_id: "acme", rpc_method: "tools/call", tool_name: "search", events: 1200, decision: "allow", policy_version: "v1", http_failures: 0, p95_latency_ms: 142, last_seen: "2026-10-25T03:00:00Z", trace_id: "a".repeat(32) }],
        actors: [{ human_id: "user-one", agent_id: "agent-one", events: 1200, unique_servers: 3, unique_tools: 1, denied: 60 }],
        facets: { servers: ["docs-search"], methods: ["tools/call", "tools/list"], tools: ["search"], policy_versions: ["v1"] },
      } });
    }
    if (path === "/runtime/teams") return route.fulfill({ json: { teams: [{ id: "acme", slug: "acme", name: "Acme", namespace: "team-acme" }] } });
    if (path === "/events") return route.fulfill({ json: { events: [] } });
    return route.fulfill({ json: {} });
  });
  return queries;
}

for (const width of [1440, 390]) for (const theme of ["dark", "light"]) {
  test(`analytics hierarchy and filters at ${width}px in ${theme}`, async ({ page }) => {
    const queries = await fixture(page);
    const errors: string[] = [];
    page.on("pageerror", error => errors.push(error.message));
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/#/admin/requests");
    await expect(page.getByTestId("analytics-stats")).toContainText("1,200");
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme; }, theme);
    await page.waitForTimeout(200);
    await expect(page.getByTestId("analytics-more")).toHaveAttribute("aria-expanded", "false");
    await expect(page.getByTestId("analytics-method")).toHaveCount(0);
    await expect(page.getByRole("region", { name: "Policy outcomes", exact: true })).toContainText("5%");
    await expect(page.getByTestId("usage-series")).toBeVisible();
    await expect(page.getByTestId("analytics-requests-table").locator("thead th")).toHaveCount(7);
    await expect(page.getByRole("link", { name: "Open trace" })).toHaveAttribute("href", /\/grafana\/explore\?left=/);
    await page.addScriptTag({ content: readFileSync(new URL("../node_modules/axe-core/axe.min.js", import.meta.url), "utf8") });
    const violations = await page.evaluate(async () => (await (window as unknown as { axe: { run: (selector: string) => Promise<{ violations: unknown[] }> } }).axe.run(".analytics-page")).violations);
    expect(violations).toEqual([]);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: test.info().outputPath(`analytics-${width}-${theme}.png`), fullPage: true });
    await page.getByTestId("analytics-more").click();
    await page.getByTestId("analytics-http-status").selectOption("5xx");
    await expect.poll(() => queries.some(query => query.get("http_status") === "5xx")).toBe(true);
    await page.getByTestId("analytics-more").click();
    await expect(page.getByTestId("analytics-more")).toContainText("1");
    await page.getByTestId("analytics-clear").click();
    await expect(page.getByTestId("analytics-more")).not.toContainText("1");
    await page.getByTestId("analytics-scope").selectOption("tenant");
    await page.getByTestId("analytics-team").selectOption("acme");
    await expect.poll(() => queries.some(query => query.get("team_id") === "acme")).toBe(true);
    await page.getByTestId("analytics-clear").click();
    await page.addStyleTag({ content: ".analytics-page * { line-height: 1.5 !important; letter-spacing: .12em !important; word-spacing: .16em !important; } .analytics-page p { margin-bottom: 2em !important; }" });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await expect(page.getByTestId("analytics-more")).toBeVisible();
    expect(errors).toEqual([]);
  });
}

test("analytics outage does not hide the independent gateway audit", async ({ page }) => {
  await fixture(page, true);
  await page.goto("/#/admin/requests");
  await expect(page.getByTestId("analytics-error")).toBeVisible();
  await expect(page.getByTestId("analytics-stats")).toHaveCount(0);
  await page.getByTestId("requests-section-decisions").locator("summary").click();
  await page.getByRole("button", { name: "Load gateway decisions" }).click();
  await expect(page.getByTestId("gateway-audit-table")).toBeVisible();
});
