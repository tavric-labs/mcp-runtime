import { expect, test } from "@playwright/test";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const { workspaceTeam, verifyWorkspaceMemberships, openWorkspaceScope, platformMetric, verifyPlatformTotals } = require("../../../../test/e2e/workspace_browser_views.cjs");

// Exercise the exact staging selectors against the rendered console. API
// fixtures establish driver compatibility; real role authority remains a
// separate assertion in the disposable-VM staging journey.
for (const role of ["member", "owner", "admin"]) {
  test(`staging workspace checks match the ${role} console`, async ({ page }) => {
    const team = { id: "team-acme", slug: "acme", name: "Acme Engineering", namespace: "team-acme", role: role === "member" ? "member" : "owner" };
    const counts = { Teams: 12, Users: 1234, "Agent identities": 7, "Active sessions": 3, Servers: 4 };
    await page.route("**/config.js", route => route.fulfill({ contentType: "application/javascript", body: "" }));
    await page.route("**/auth/status", route => route.fulfill({ json: { authenticated: true, principal: { role: role === "admin" ? "admin" : "user", subject: "fixture" } } }));
    await page.route("**/api/ui/v1/**", route => {
      const path = new URL(route.request().url()).pathname.replace("/api/ui/v1", "");
      let json: unknown = {};
      if (path === "/runtime/workspace/teams") json = { teams: [team] };
      else if (path === "/runtime/workspace/servers") json = { servers: [], deployment_attribution: "unavailable" };
      else if (path === "/runtime/workspace/access") json = { grants: [], sessions: [] };
      else if (path === "/runtime/teams/acme/agents") json = { agents: [] };
      else if (path === "/runtime/namespaces") json = { namespaces: [] };
      else if (path === "/runtime/servers") json = { servers: [] };
      else if (path === "/runtime/tools") json = { tools: [] };
      else if (path === "/user/analytics/usage") json = { totals: {}, requests: [], recent: [] };
      else if (path === "/admin/overview/directory") json = { teams: 12, users: 1234, agents: 7, active_sessions: 3 };
      else if (path === "/admin/overview/servers") json = { servers: 4, failures: [] };
      else if (path === "/runtime/components") json = { components: [] };
      else if (path === "/admin/operations") json = { audit_logs: [] };
      return route.fulfill({ json });
    });
    await page.goto("/#/workspace");
    await verifyWorkspaceMemberships(page, [team]);
    await openWorkspaceScope(page);
    await openWorkspaceScope(page); // Checking it again must not collapse it.
    await expect(page.getByText(/Deployed by me: unavailable\./)).toBeVisible();
    await expect(workspaceTeam(page, team).getByRole("link", { name: "Manage members", exact: true })).toHaveCount(role === "member" ? 0 : 1);
    if (role !== "admin") {
      await expect(page.getByText("Gateway activity across your namespaces in the last 7 days.", { exact: true })).toBeVisible();
      return;
    }
    await page.getByRole("link", { name: "Platform overview", exact: true }).first().click();
    await verifyPlatformTotals(page, counts);
    await page.route("**/api/ui/v1/admin/overview/directory", route => route.fulfill({ status: 503, json: {} }));
    await page.reload();
    await expect(page.getByTestId("overview-directory-error")).toBeVisible();
    await expect(platformMetric(page, "Servers")).toHaveText("4");
    await expect(platformMetric(page, "Teams")).toHaveText("—");
    await page.unroute("**/api/ui/v1/admin/overview/directory");
    await page.route("**/api/ui/v1/admin/operations**", route => route.fulfill({ status: 503, json: {} }));
    await page.reload();
    await expect(page.getByTestId("overview-audit-error")).toBeVisible();
    await verifyPlatformTotals(page, counts);
  });
}
