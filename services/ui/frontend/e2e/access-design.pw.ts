import { expect, test, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";

async function fixture(page: Page, role = "admin", empty = false) {
  const writes: string[] = [];
  await page.route("**/config.js", route => route.fulfill({ contentType: "application/javascript", body: "" }));
  await page.route("**/auth/status", route => route.fulfill({ json: { authenticated: true, principal: { role, subject: "me" } } }));
  await page.route("**/api/ui/v1/**", route => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace("/api/ui/v1", "");
    if (request.method() !== "GET") writes.push(path);
    const grant = { name: "build-read", namespace: "team-acme", serverRef: { name: "docs" }, subject: { humanID: "alice@example.test", agentID: "agt_build", teamID: "acme" }, mode: "enforce", maxTrust: "low", allowedSideEffects: ["read"], disabled: false };
    const session = { name: "build-session", namespace: "team-acme", serverRef: { name: "docs" }, subject: { humanID: "alice@example.test", agentID: "agt_build" }, consentedTrust: "low", revoked: false };
    const json = path === "/runtime/grants" ? { grants: empty ? [] : [grant, { ...grant, name: "expired", expiresAt: "2020-01-01T00:00:00Z" }, { ...grant, name: "disabled", disabled: true }] } :
      path === "/runtime/sessions" ? { sessions: empty ? [] : [session, { ...session, name: "expired-session", expiresAt: "2020-01-01T00:00:00Z" }] } :
      path === "/runtime/teams" ? { teams: [{ id: "acme", slug: "acme", name: "Acme", namespace: "team-acme", role: "member" }] } :
      path === "/runtime/namespaces" ? { namespaces: [{ namespace: "team-acme" }] } :
      path === "/runtime/servers" ? { servers: [] } : path === "/runtime/tools" ? { tools: [] } : {};
    return route.fulfill({ json });
  });
  return writes;
}

for (const width of [1440, 390]) for (const theme of ["dark", "light"]) {
  test(`access record hierarchy and safe actions at ${width}px in ${theme}`, async ({ page }) => {
    const writes = await fixture(page);
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/#/access");
    await expect(page.getByTestId("grants-table")).toContainText("build-read");
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme; }, theme);
    await page.waitForTimeout(200);
    await expect(page.getByTestId("grants-table")).toContainText("Human");
    await expect(page.getByTestId("grants-table")).toContainText("alice@example.test");
    await expect(page.locator(".access-summary > div").nth(1)).toHaveText("Inactive grants2");
    await expect(page.getByTestId("grant-toggle").first()).toBeHidden();
    await page.getByLabel("Actions for grant build-read", { exact: true }).click();
    await page.getByRole("button", { name: "Disable", exact: true }).click();
    await expect(page.getByTestId("access-confirm")).toContainText("build-read");
    await page.getByTestId("access-confirm-cancel").click();
    expect(writes).toEqual([]);
    await page.getByLabel("Actions for grant build-read", { exact: true }).click();
    await page.addScriptTag({ content: readFileSync(new URL("../node_modules/axe-core/axe.min.js", import.meta.url), "utf8") });
    const violations = await page.evaluate(async () => (await (window as unknown as { axe: { run: (selector: string) => Promise<{ violations: unknown[] }> } }).axe.run(".access-page")).violations);
    expect(violations).toEqual([]);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.evaluate(() => {
      document.querySelectorAll(".table-region").forEach(region => { region.scrollLeft = 0; });
      window.scrollTo(0, 0);
    });
    await page.screenshot({ path: test.info().outputPath(`access-${width}-${theme}.png`), fullPage: true });
  });
}

test("empty access directories guide creation without empty table headers", async ({ page }) => {
  await fixture(page, "admin", true);
  await page.goto("/#/access");
  await expect(page.getByTestId("grants-empty")).toBeVisible();
  await expect(page.getByTestId("sessions-empty")).toBeVisible();
  await expect(page.getByTestId("grants-table")).toHaveCount(0);
  await page.getByRole("button", { name: "Create a grant", exact: true }).click();
  await expect(page.getByTestId("grant-create-form")).toBeVisible();
  await expect(page.getByTestId("grant-name")).toBeFocused();
  await expect(page.getByTestId("grant-name")).toBeInViewport();
  await page.getByTestId("grant-create-form").getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(page.getByTestId("grant-create-form")).toHaveCount(0);
});

test("a member sees subjects and states without row management actions", async ({ page }) => {
  const writes = await fixture(page, "user");
  await page.goto("/#/access");
  await expect(page.getByTestId("grants-table")).toBeVisible();
  await expect(page.getByTestId("access-namespace")).toHaveValue("team-acme");
  await expect(page.getByTestId("grant-create-toggle")).toHaveCount(0);
  await expect(page.locator(".access-row-actions")).toHaveCount(0);
  expect(writes).toEqual([]);
});
