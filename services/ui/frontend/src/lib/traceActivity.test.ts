import { describe, expect, it } from "vitest";

import { isTraceID, mergeTraceActivity } from "./traceActivity";

describe("mergeTraceActivity", () => {
  it("merges audit entries and tool calls newest first with their trace ids", () => {
    const rows = mergeTraceActivity(
      [
        { action: "grant.create", resource: "g-1", status: "success", source: "cli", auth_identity: "alice", trace_id: "a".repeat(32), request_id: "req-1", created_at: "2026-10-28T10:00:00Z" },
        { action: "login", resource: "u-1", status: "success", source: "ui", created_at: "2026-10-28T09:00:00Z" },
      ],
      [{ timestamp: "2026-10-28T10:00:05Z", namespace: "team-a", server: "files", tool_name: "read", decision: "allow", trace_id: "b".repeat(32) }]
    );
    expect(rows.map((row) => [row.kind, row.operation, row.traceId])).toEqual([
      ["tool", "read", "b".repeat(32)],
      ["cli", "grant.create", "a".repeat(32)],
      ["console", "login", ""],
    ]);
    expect(rows[0].target).toBe("team-a/files");
    expect(rows[1].requestId).toBe("req-1");
  });

  it("reads the kind from audit sources recorded as kind:identity", () => {
    const rows = mergeTraceActivity(
      [
        { action: "agent.created", resource: "a", status: "success", source: "cli:platform_jwt" },
        { action: "login", resource: "u", status: "success", source: "ui:password" },
        { action: "login", resource: "u", status: "success", source: "api:password" },
      ],
      []
    );
    expect(rows.map((row) => row.kind).sort()).toEqual(["api", "cli", "console"]);
  });

  it("labels an unknown audit source as a platform operation", () => {
    const [row] = mergeTraceActivity([{ action: "x", resource: "y", status: "ok", source: "operator" }], []);
    expect(row.kind).toBe("platform");
  });
});

describe("isTraceID", () => {
  it("accepts only 32 lowercase hex digits that are not all zero", () => {
    expect(isTraceID("4bf92f3577b34da6a3ce929d0e0e4736")).toBe(true);
    expect(isTraceID("4BF92F3577B34DA6A3CE929D0E0E4736")).toBe(false);
    expect(isTraceID("0".repeat(32))).toBe(false);
    expect(isTraceID("abc")).toBe(false);
  });
});
