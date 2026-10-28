import { eventToolName, eventRpcMethod } from "../api/types";
import type { AuditLogEntry, GatewayEvent } from "../api/types";

export type TraceActivityKind = "tool" | "cli" | "console" | "platform";

export type TraceActivityRow = {
  key: string;
  time: string;
  kind: TraceActivityKind;
  operation: string;
  target: string;
  actor: string;
  outcome: string;
  traceId: string;
  requestId: string;
};

export const TRACE_KIND_LABEL: Record<TraceActivityKind, string> = {
  tool: "Tool call",
  cli: "CLI",
  console: "Console",
  platform: "Platform",
};

export function isTraceID(value: string): boolean {
  return /^[0-9a-f]{32}$/.test(value) && value !== "0".repeat(32);
}

function auditKind(source: string | undefined): TraceActivityKind {
  const value = (source || "").trim().toLowerCase();
  if (value === "cli") return "cli";
  if (value === "ui" || value === "console") return "console";
  return "platform";
}

/**
 * Merges platform audit entries and gateway tool-call events into one list,
 * newest first. Rows without a timestamp sort last.
 */
export function mergeTraceActivity(audit: AuditLogEntry[], events: GatewayEvent[]): TraceActivityRow[] {
  const rows: TraceActivityRow[] = [];
  audit.forEach((entry, index) => {
    rows.push({
      key: `audit-${entry.created_at || ""}-${index}`,
      time: entry.created_at || "",
      kind: auditKind(entry.source),
      operation: entry.action || "—",
      target: entry.resource || "—",
      actor: entry.auth_identity || entry.user_id || "—",
      outcome: entry.status || "unknown",
      traceId: entry.trace_id || "",
      requestId: entry.request_id || "",
    });
  });
  events.forEach((event, index) => {
    const payload = event.payload || {};
    rows.push({
      key: `event-${event.timestamp || ""}-${index}`,
      time: event.timestamp || "",
      kind: "tool",
      operation: eventToolName(event) || eventRpcMethod(event) || event.event_type || "—",
      target: `${event.namespace || "?"}/${event.server || "?"}`,
      actor: event.subject_team_id || (typeof payload.subject_team_id === "string" ? payload.subject_team_id : "") || "—",
      outcome: event.decision || "unknown",
      traceId: event.trace_id || "",
      requestId: "",
    });
  });
  return rows.sort((a, b) => (Date.parse(b.time) || 0) - (Date.parse(a.time) || 0));
}
