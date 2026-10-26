import { expect, test } from "@playwright/test";

// These browser fixtures cover the sign-in transition; the Go regression tests
// cover forward-auth decisions. Production acceptance exercises the real gate.
for (const role of ["admin", "user"]) {
  test(`Grafana return after platform ${role} sign-in`, async ({ page }) => {
    await page.route("**/config.js", route => route.fulfill({ contentType: "application/javascript", body: "" }));
    await page.route("**/auth/status", route => route.fulfill({ json: { authenticated: false } }));
    await page.route("**/auth/login", route => route.fulfill({ json: { authenticated: true, principal: { role } } }));
    await page.route("**/api/ui/v1/**", route => route.fulfill({ json: {} }));
    await page.route("**/grafana/explore?**", route => route.fulfill({ contentType: "text/html", body: "<h1>Grafana trace</h1>" }));
    const destination = "/grafana/explore?left=%7B%22trace%22%3A%22abc%22%7D";
    await page.goto(`/#/signin?${new URLSearchParams({ return_to: destination })}`);
    await expect(page.getByText("Sign in with a platform administrator account to open Grafana. Grafana has a separate login after this step.")).toBeVisible();
    await page.getByTestId("login-email").fill(`${role}@example.com`);
    await page.getByTestId("login-password").fill("test-password");
    await page.getByTestId("login-submit").click();
    if (role === "admin") {
      await expect(page).toHaveURL(new URL(destination, "http://127.0.0.1:4189").href);
      await expect(page.getByRole("heading", { name: "Grafana trace" })).toBeVisible();
    } else {
      await expect(page.getByTestId("login-error")).toContainText("This account is not a platform administrator.");
      expect(new URL(page.url()).hash).toContain("#/signin?");
    }
  });
}
