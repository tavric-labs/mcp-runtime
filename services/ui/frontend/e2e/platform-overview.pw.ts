import { expect, test, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";

// Browser fixtures verify UX and request shape; they do not prove live authorization.
async function fixture(page: Page, role = "admin", teamRole = "owner", auditFails = false) {
  const requests: { path: string; method: string; body: unknown }[] = [];
  let created = false;
  let failCreate = true;
  await page.route("**/config.js", route => route.fulfill({ contentType: "application/javascript", body: "" }));
  await page.route("**/auth/status", route => route.fulfill({ json: { authenticated: Boolean(role), principal: role ? { role, subject: "qa-user" } : undefined } }));
  await page.route("**/api/ui/v1/**", route => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace("/api/ui/v1", "");
    requests.push({ path, method: request.method(), body: request.postDataJSON() });
    let json: unknown = {};
    if (path === "/admin/overview/directory") json = { teams: 2, users: 1234, agents: 48, active_sessions: 27 };
    else if (path === "/admin/overview/servers") json = { servers: 36, failures: [{ namespace: "team-acme", name: "broken", status: "Error" }] };
    else if (path === "/runtime/components") json = { components: [
      { key: "runtime", display: "Runtime API", status: "Ready", ready: "1/1", namespace: "mcp-platform", resource: "runtime-api", kind: "Deployment" },
      { key: "gateway", display: "MCP Gateway", status: "Degraded", ready: "0/1", namespace: "mcp-platform", resource: "gateway", kind: "Deployment", message: "Waiting for readiness" },
    ] };
    else if (path === "/admin/operations") {
      if (auditFails) return route.fulfill({ status: 503, json: { error: "Audit unavailable" } });
      json = { audit_logs: [{ created_at: "2026-10-25T00:00:00Z", action: "agent.create", resource: "Build agent", status: "success" }] };
    }
    else if (path === "/runtime/teams") json = { teams: [
      { id: "t1", slug: "acme", name: "Acme", role: teamRole },
      { id: "t2", slug: "other", name: "Other team", role: "member" },
    ] };
    else if (path === "/runtime/teams/acme/agents" && request.method() === "POST") {
      if (failCreate) { failCreate = false; return route.fulfill({ status: 503, json: { error: "Please retry creation" } }); }
      created = true;
      return route.fulfill({ status: 201, json: { agent: { id: "agt-created", name: "Build agent", team_id: "t1", team_slug: "acme", status: "active" } } });
    }
    else if (path.endsWith("/agents")) json = { agents: created && path.includes("/acme/") ? [{ id: "agt-created", name: "Build agent", team_id: "t1", team_slug: "acme", status: "active" }] : [] };
    return route.fulfill({ json });
  });
  return requests;
}

async function noOverflow(page: Page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
}

for (const width of [1440, 390]) {
  for (const theme of ["dark", "light"]) {
    test(`unified overview at ${width}px in ${theme}`, async ({ page }) => {
      const errors: string[] = [];
      page.on("pageerror", error => errors.push(error.message));
      const requests = await fixture(page);
      await page.setViewportSize({ width, height: 900 });
      await page.goto("/#/admin/overview");
      await expect(page.getByTestId("platform-components")).toContainText("Runtime API");
      await expect(page.getByRole("group", { name: "Platform-wide totals" })).toContainText("1,234");
      await expect(page.getByRole("link", { name: "team-acme/broken" })).toHaveAttribute("href", "#/servers?namespace=team-acme&server=team-acme%2Fbroken");
      await expect(page.getByTestId("grafana-link")).toHaveAttribute("href", "/grafana");
      await expect(page.locator("h1")).toHaveCount(1);
      await expect(page.getByTestId("admin-section-platform")).toHaveCount(0);
      await page.evaluate(value => { document.documentElement.dataset.theme = value; }, theme);
      await page.waitForTimeout(200); // Let semantic color transitions settle before contrast checks.
      await page.addScriptTag({ content: readFileSync(new URL("../node_modules/axe-core/axe.min.js", import.meta.url), "utf8") });
      const violations = await page.evaluate(async () => {
        const axe = (window as unknown as { axe: { run: (context: string) => Promise<{ violations: { id: string; nodes: unknown[] }[] }> } }).axe;
        return (await axe.run(".platform-overview")).violations;
      });
      expect(violations).toEqual([]);
      await noOverflow(page);
      await page.screenshot({ path: test.info().outputPath(`overview-${width}-${theme}.png`), fullPage: true });
      const beforeRefresh = requests.length;
      await page.getByTestId("platform-refresh").click();
      await expect.poll(() => requests.slice(beforeRefresh).filter(item => ["/runtime/components", "/admin/overview/directory", "/admin/overview/servers", "/admin/operations"].includes(item.path)).length).toBe(4);
      await page.getByTestId("restart-runtime").click();
      await expect(page.getByTestId("platform-confirm")).toBeVisible();
      await page.getByTestId("platform-confirm-cancel").click();
      await page.getByText("Platform maintenance", { exact: true }).click();
      await expect(page.getByTestId("restart-all")).toBeVisible();
      expect(requests.filter(item => item.method !== "GET")).toEqual([]);
      expect(errors).toEqual([]);
    });
  }

  test(`create identity from all teams with validation and retry at ${width}px`, async ({ page }) => {
    const requests = await fixture(page);
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/#/agents");
    await page.getByRole("button", { name: "Create agent identity" }).click();
    await expect(page.getByTestId("agent-team-select")).toHaveValue("*");
    await page.getByTestId("agent-create-submit").click();
    expect(requests.filter(item => item.method === "POST")).toEqual([]);
    await page.getByRole("combobox", { name: /Identity team/ }).selectOption("acme");
    await page.getByTestId("agent-create-name").fill("  ");
    await page.getByTestId("agent-create-submit").click();
    await expect(page.getByTestId("agent-action-error")).toContainText("enter an agent name");
    await page.getByTestId("agent-create-name").fill("Build agent");
    await noOverflow(page);
    await page.screenshot({ path: test.info().outputPath(`create-agent-${width}.png`), fullPage: true });
    await page.getByTestId("agent-create-submit").click();
    await expect(page.getByTestId("agent-action-error")).toBeVisible();
    await expect(page.getByTestId("agent-create-name")).toHaveValue("Build agent");
    await page.getByTestId("agent-create-submit").click();
    await expect(page.getByTestId("agent-action-notice")).toContainText("created");
    await expect(page.getByTestId("agents-table")).toContainText("Build agent");
    await expect(page.getByTestId("agent-team-select")).toHaveValue("*");
    expect(requests.filter(item => item.method === "POST")).toEqual([
      { path: "/runtime/teams/acme/agents", method: "POST", body: { name: "Build agent" } },
      { path: "/runtime/teams/acme/agents", method: "POST", body: { name: "Build agent" } },
    ]);
    await page.getByRole("button", { name: "Create agent identity" }).click();
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(page.getByTestId("agent-create-name")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Create agent identity" })).toBeFocused();
    await noOverflow(page);
    await page.screenshot({ path: test.info().outputPath(`agents-${width}.png`), fullPage: true });
  });
}

test("team owners can create only for owned teams; members cannot create", async ({ page }) => {
  await fixture(page, "user", "owner");
  await page.goto("/#/agents");
  await page.getByRole("button", { name: "Create agent identity" }).click();
  await expect(page.getByRole("combobox", { name: /Identity team/ }).locator("option")).toHaveText(["Choose a team", "Acme"]);
});

for (const role of ["user", ""]) {
  test(`overview and creation stay gated for ${role || "signed-out"}`, async ({ page }) => {
    const requests = await fixture(page, role, "member");
    await page.goto("/#/admin/overview");
    if (role) await expect(page.getByRole("heading", { name: "Servers", exact: true })).toBeVisible();
    else await expect(page.getByTestId("catalog-signed-out")).toBeVisible();
    await expect(page.locator(".platform-overview")).toHaveCount(0);
    await expect(page.getByTestId("admin-section-overview")).toHaveCount(0);
    expect(requests.filter(item => item.path.startsWith("/admin/") || item.path === "/runtime/components")).toEqual([]);
    await page.goto("/#/agents");
    if (role) await expect(page.getByRole("heading", { name: "Agent Management" })).toBeVisible();
    else await expect(page.getByTestId("catalog-signed-out")).toBeVisible();
    await expect(page.getByRole("button", { name: "Create agent identity" })).toHaveCount(0);
  });
}

test("audit outage leaves health and counts available; old health URL opens overview", async ({ page }) => {
  await fixture(page, "admin", "owner", true);
  await page.goto("/#/admin/platform");
  await expect(page.getByRole("heading", { name: "Platform overview", exact: true })).toBeVisible();
  await expect(page.getByTestId("overview-audit-error")).toBeVisible();
  await expect(page.getByTestId("platform-components")).toContainText("Runtime API");
  await expect(page.getByRole("group", { name: "Platform-wide totals" })).toContainText("1,234");
  await expect(page.getByTestId("admin-section-overview")).toHaveAttribute("aria-current", "page");
});
