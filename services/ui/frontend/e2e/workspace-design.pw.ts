import { expect, test, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";

async function fixture(page: Page) {
  const reads: string[] = [];
  const team = { id: "team-acme", slug: "acme", name: "Acme Engineering", namespace: "team-acme", role: "owner" };
  const agent = { id: "agt_01arz3ndektsv4rrffq69g5fav", name: "Build assistant", team_id: team.id, team_slug: team.slug, status: "active", created_by: "creator-user" };
  await page.route("**/config.js", route => route.fulfill({ contentType: "application/javascript", body: "" }));
  await page.route("**/auth/status", route => route.fulfill({ json: { authenticated: true, principal: { role: "user", subject: "me" } } }));
  await page.route("**/api/ui/v1/**", route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/ui/v1", "");
    reads.push(path);
    let json: unknown = {};
    if (path === "/runtime/workspace/teams" || path === "/runtime/teams") json = { teams: [team] };
    else if (path === "/runtime/teams/acme/agents") json = { agents: [agent] };
    else if (path === `/runtime/agents/${agent.id}`) json = { agent, grants: [], sessions: [], can_manage: true, certificates: { recorded: 0, recent: [], since: "2026-10-18T00:00:00Z", until: "2026-10-25T00:00:00Z" } };
    else if (path === "/runtime/workspace/servers") json = { servers: [{ namespace: "team-acme", name: "docs", status: "Ready", can_manage: true }], deployment_attribution: "unavailable" };
    else if (path === "/runtime/workspace/access") json = { grants: [], sessions: [{ name: "old-session", namespace: "team-acme", revoked: false, expiresAt: "2020-01-01T00:00:00Z" }] };
    else if (path === "/runtime/namespaces") json = { namespaces: [{ namespace: "team-acme" }] };
    else if (path === "/runtime/servers") json = { servers: [] };
    else if (path === "/runtime/tools") json = { tools: [] };
    else if (path === "/user/analytics/usage") json = { filters: { agent_id: url.searchParams.get("agent_id") }, totals: { events: 0, allowed: 0, denied: 0, http_failures: 0 }, requests: [], recent: [] };
    return route.fulfill({ json });
  });
  return { reads, agent };
}

async function accessible(page: Page, selector: string) {
  await page.addScriptTag({ content: readFileSync(new URL("../node_modules/axe-core/axe.min.js", import.meta.url), "utf8") });
  const violations = await page.evaluate(async selector => {
    const axe = (window as unknown as { axe: { run: (selector: string) => Promise<{ violations: unknown[] }> } }).axe;
    return (await axe.run(selector)).violations;
  }, selector);
  expect(violations).toEqual([]);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
}

for (const width of [1440, 390]) for (const theme of ["dark", "light"]) {
  test(`workspace and agent design at ${width}px in ${theme}`, async ({ page }) => {
    const errors: string[] = [];
    page.on("pageerror", error => errors.push(error.message));
    const { reads, agent } = await fixture(page);
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/#/workspace");
    await expect(page.getByRole("link", { name: /Build assistant/ })).toBeVisible();
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme; }, theme);
    await page.waitForTimeout(200);
    await expect(page.getByRole("heading", { name: "Acme Engineering", exact: true })).toHaveCount(1);
    await expect(page.getByRole("region", { name: "Acme Engineering team" })).toContainText("Build assistant");
    await expect(page.getByText("Expired", { exact: true })).toBeVisible();
    await expect(page.getByRole("link", { name: "Manage members" })).toHaveAttribute("href", "#/workspace/team?team=acme");
    await accessible(page, ".workspace-home");
    await page.screenshot({ path: test.info().outputPath(`workspace-${width}-${theme}.png`), fullPage: true });
    const readsBeforeAgent = reads.length;
    await page.getByRole("link", { name: /Build assistant/ }).click();
    await expect(page.getByTestId("agent-detail")).toBeVisible();
    await expect(page.getByRole("tab", { name: "Access & sessions" })).toHaveAttribute("aria-selected", "true");
    await expect(page.getByTestId("agent-grants-empty")).toBeVisible();
    await expect(page.getByTestId("agent-grants-table")).toHaveCount(0);
    await expect(page.locator(".agent-stable-id code")).toHaveText(agent.id);
    expect(reads.slice(readsBeforeAgent).filter(path => path === "/user/analytics/usage")).toEqual([]);
    await accessible(page, ".agent-detail-page");
    await page.screenshot({ path: test.info().outputPath(`agent-access-${width}-${theme}.png`), fullPage: true });
    await page.getByRole("tab", { name: "Access & sessions" }).focus();
    await page.keyboard.press("ArrowRight");
    await expect(page.getByRole("tab", { name: "Connect", exact: true })).toBeFocused();
    await expect(page).toHaveURL(/view=connect/);
    await expect(page.getByRole("heading", { name: "Connect this agent", exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Copy adapter command" })).toBeVisible();
    await expect(page.locator(".agent-connect pre").last()).toContainText(`--agent ${agent.id}`);
    await accessible(page, ".agent-detail-page");
    await page.screenshot({ path: test.info().outputPath(`agent-connect-${width}-${theme}.png`), fullPage: true });
    await page.reload();
    await expect(page.getByRole("tab", { name: "Connect", exact: true })).toHaveAttribute("aria-selected", "true");
    await page.getByRole("tab", { name: "Activity", exact: true }).click();
    await expect(page.getByRole("group", { name: "Agent tool call totals" })).toBeVisible();
    await expect(page).toHaveURL(/view=activity/);
    await page.getByRole("tab", { name: "Certificates", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Recorded certificate issuance", exact: true })).toBeVisible();
    await expect(page).toHaveURL(/view=certificates/);
    await accessible(page, ".agent-detail-page");
    expect(errors).toEqual([]);
  });
}
