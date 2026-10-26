import { afterEach, describe, expect, it } from "vitest";
import { grafanaReturnPath } from "./grafanaReturn";

afterEach(() => { delete window.MCP_PUBLIC_ROUTES; });

describe("Grafana sign-in return paths", () => {
  it("preserves same-origin Grafana deep links", () => {
    expect(grafanaReturnPath("/grafana/explore?left=%7B%22trace%22%3A%22abc%22%7D")).toBe("/grafana/explore?left=%7B%22trace%22%3A%22abc%22%7D");
    expect(grafanaReturnPath("/grafana/login")).toBe("/grafana/login");
  });
  it.each([
    undefined, "", "https://evil.example/grafana/", "//evil.example/grafana/",
    "/\\evil.example/grafana/", "/grafana/../../auth/login", "/grafana/%2e%2e/auth/login",
    "/grafana/%5cevil", "/grafana/%0aevil", "/auth/logout", "/grafana-other/", "/grafana/api/datasources",
    "/grafana/%61pi/datasources", "/grafana/%", "/grafana/\nlogin",
  ])("rejects unsafe or unrelated target %s", value => {
    expect(grafanaReturnPath(value)).toBe("");
  });
  it("uses the configured Grafana path", () => {
    window.MCP_PUBLIC_ROUTES = { prefix: "/console", grafana: "/observability", docs: "/docs", registry: "/registry" };
    expect(grafanaReturnPath("/observability/explore?orgId=1")).toBe("/observability/explore?orgId=1");
    expect(grafanaReturnPath("/grafana/")).toBe("");
  });
});
