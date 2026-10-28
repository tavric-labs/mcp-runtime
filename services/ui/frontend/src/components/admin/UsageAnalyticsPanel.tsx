import { useMemo, useState } from "react";
import { grafanaPath } from "../../api/config";
import { AsyncSection } from "./AsyncSection";
import { StatusBadge, decisionTone } from "../../ui/Badge";
import { Button, ButtonLink } from "../../ui/Button";
import { DataTable, buildColumns } from "../../ui/DataTable";
import { SelectField, TextField } from "../../ui/Field";
import { FilterBar } from "../../ui/FilterBar";
import { PageHeader } from "../../ui/PageHeader";
import { EmptyState } from "../../ui/States";
import { Icon } from "../../ui/Icon";
import { UsageSeries } from "../usage/UsageSeries";
import { GatewayAuditPanel } from "./GatewayAuditPanel";
import { formatAbsolute, formatTimestamp } from "../../lib/format";
import { USAGE_REFRESH_MS, useAdminReload, useTeams, useUsage } from "../../hooks/useAdminData";
import type {
  RequestUsage,
  UsageFilterParams,
  UsageResponse,
} from "../../api/types";

type Props = { onSignIn: () => void };
type Actor = NonNullable<UsageResponse["actors"]>[number];
const WINDOW_OPTIONS = [
  { value: "1", label: "Last hour" },
  { value: "6", label: "Last 6 hours" },
  { value: "24", label: "Last 24 hours" },
  { value: "168", label: "Last 7 days" },
  { value: "720", label: "Last 30 days" },
];
const options = (values: string[], label: string, selected = "") => [
  { value: "", label },
  ...[...new Set([...values, selected].filter(Boolean))]
    .sort()
    .map((value) => ({ value, label: value })),
];
const ms = (value?: number) =>
  value === undefined
    ? "—"
    : `${value.toLocaleString(undefined, { maximumFractionDigits: 1 })} ms`;
const bytes = (value?: number) =>
  value === undefined
    ? "—"
    : value < 1024
      ? `${value} B`
      : value < 1048576
        ? `${(value / 1024).toFixed(1)} KiB`
        : `${(value / 1048576).toFixed(1)} MiB`;

// `at` centres the Explore time range on when the request ran, so an old trace
// is not looked up in the default last-hour window.
const EXPLORE_WINDOW_MS = 60 * 60 * 1000;

function exploreRange(at?: string): { from: string; to: string } {
  const centre = Date.parse(at || "");
  if (!Number.isFinite(centre)) return { from: "now-1h", to: "now" };
  return { from: String(centre - EXPLORE_WINDOW_MS), to: String(centre + EXPLORE_WINDOW_MS) };
}

export function traceExploreURL(traceID: string, at?: string): string | undefined {
  if (!/^[0-9a-f]{32}$/i.test(traceID)) return undefined;
  const left = {
    datasource: "tempo",
    queries: [
      {
        refId: "A",
        queryType: "traceId",
        query: traceID,
        datasource: { type: "tempo", uid: "tempo" },
      },
    ],
    range: exploreRange(at),
  };
  return `${grafanaPath()}/explore?left=${encodeURIComponent(JSON.stringify(left))}`;
}

export function UsageAnalyticsPanel({ onSignIn }: Props) {
  const [limit, setLimit] = useState("25");
  const [filters, setFilters] = useState<UsageFilterParams>({
    window_hours: "24",
  });
  const [teamSearch, setTeamSearch] = useState("");
  const [moreFilters, setMoreFilters] = useState(false);
  const reload = useAdminReload();
  const query = useUsage(true, limit, filters);
  const teamsQuery = useTeams(true);
  const usage = query.data;
  const teams = teamsQuery.data || [];
  const facets = usage?.facets;
  const change = (key: keyof UsageFilterParams, value: string) =>
    setFilters((current) => ({ ...current, [key]: value }));
  const teamNames = new Map(
    teams.map((team) => [team.id, team.name || team.slug]),
  );
  const matchingTeams = teams.filter(
    (team) =>
      team.id === filters.team_id ||
      `${team.name} ${team.slug}`
        .toLowerCase()
        .includes(teamSearch.toLowerCase().trim()),
  );
  const actorColumns = useMemo(
    () =>
      buildColumns<Actor>([
        {
          id: "human",
          header: "Human",
          rowHeader: true,
          sortValue: (r) => r.human_id || "",
          cell: (r) => r.human_id || "Anonymous / unreported",
        },
        {
          id: "agent",
          header: "Agent",
          sortValue: (r) => r.agent_id || "",
          cell: (r) => r.agent_id || "—",
        },
        {
          id: "events",
          header: "Requests",
          numeric: true,
          sortValue: (r) => r.events,
          cell: (r) => r.events.toLocaleString(),
        },
        {
          id: "servers",
          header: "Servers",
          numeric: true,
          sortValue: (r) => r.unique_servers,
          cell: (r) => String(r.unique_servers),
        },
        {
          id: "denied",
          header: "Denied",
          numeric: true,
          sortValue: (r) => r.denied,
          cell: (r) => r.denied.toLocaleString(),
        },
      ]),
    [],
  );
  const requestColumns = useMemo(() => buildColumns<RequestUsage>([
    { id: "server", header: "Server", rowHeader: true, sortValue: r => `${r.namespace}/${r.server}`, cell: r => <><strong title={teamNames.get(r.team_id || "")}>{r.server}</strong><span className="cell-detail">{r.namespace}</span></> },
    { id: "method", header: "Method / tool", sortValue: r => `${r.rpc_method}/${r.tool_name || ""}`, cell: r => <><code>{r.rpc_method || "HTTP transport"}</code>{r.tool_name ? <span className="cell-detail">{r.tool_name}</span> : null}</> },
    { id: "events", header: "Requests", numeric: true, sortValue: r => r.events, cell: r => r.events.toLocaleString() },
    { id: "decision", header: "Decision", sortValue: r => r.decision, cell: r => <><StatusBadge tone={decisionTone(r.decision)}>{r.decision || "unknown"}</StatusBadge><span className="cell-detail">{r.policy_version || "Policy unreported"}</span></> },
    { id: "errors", header: "HTTP errors", numeric: true, sortValue: r => r.http_failures, cell: r => r.http_failures.toLocaleString() },
    { id: "latency", header: "p95 latency", numeric: true, sortValue: r => r.p95_latency_ms, cell: r => ms(r.p95_latency_ms) },
    { id: "last", header: "Latest request", sortValue: r => Date.parse(r.last_seen), cell: r => { const url = traceExploreURL(r.trace_id || ""); return <><time dateTime={r.last_seen} title={formatAbsolute(r.last_seen)}>{formatTimestamp(r.last_seen)}</time>{url ? <a className="cell-detail analytics-trace-link" href={url} target="_blank" rel="noreferrer" title={r.trace_id}>Open trace <Icon name="external" size={12} /></a> : null}</>; } },
  ]), [teamsQuery.data]);
  const totals = usage?.totals;
  const reset = () => { setFilters({ window_hours: "24" }); setTeamSearch(""); };
  const advancedCount = [filters.rpc_method, filters.tool_name, filters.policy_version, filters.http_status].filter(Boolean).length;
  const activeFilters = Object.entries(filters).filter(([key, value]) => key !== "window_hours" && value).length;
  const events = totals?.events;
  const deniedRate = events && totals?.denied !== undefined ? (totals.denied / events * 100).toLocaleString(undefined, { maximumFractionDigits: 1 }) : undefined;
  const decisionRows = totals ? [
    { label: "Allowed", value: totals.allowed, tone: "success" },
    { label: "Denied", value: totals.denied, tone: "danger" },
    { label: "Other decisions", value: typeof events === "number" && typeof totals.allowed === "number" && typeof totals.denied === "number" ? Math.max(0, events - totals.allowed - totals.denied) : undefined, tone: "neutral" },
  ] : [];
  const timeLabel = WINDOW_OPTIONS.find(option => option.value === filters.window_hours)?.label || "Selected time range";
  return <div className="analytics-page">
    <PageHeader title="Usage analytics" breadcrumb={[{ label: "Administration" }, { label: "Usage analytics" }]}
      description="Understand gateway traffic, policy decisions, and request reliability."
      actions={<><ButtonLink variant="secondary" href={`${grafanaPath()}/explore`} target="_blank" rel="noreferrer" trailingIcon="external" data-testid="analytics-detailed-activity">Detailed activity</ButtonLink><Button variant="secondary" icon="refresh" onClick={reload} busy={query.isFetching && !query.isPending} data-testid="analytics-refresh">Refresh</Button></>} />
    <section className="analytics-filter-panel" aria-label="Filter analytics">
      <FilterBar label="Analytics filters">
        <div className="analytics-primary-filters">
          <SelectField label="Time range" value={filters.window_hours || "24"} options={WINDOW_OPTIONS} onChange={e => change("window_hours", e.target.value)} data-testid="analytics-window" />
          <SelectField label="Scope" value={filters.scope || ""} options={[{ value: "", label: "All scopes" }, { value: "public", label: "Public" }, { value: "org", label: "Organization" }, { value: "tenant", label: "Teams" }]} onChange={e => { setFilters(current => ({ ...current, scope: e.target.value, team_id: "", server: "", rpc_method: "", tool_name: "" })); setTeamSearch(""); }} data-testid="analytics-scope" />
          <SelectField label="Server" value={filters.server || ""} options={options(facets?.servers || [], "All servers", filters.server)} onChange={e => change("server", e.target.value)} data-testid="analytics-server" />
          <SelectField label="Policy decision" value={filters.decision || ""} options={[{ value: "", label: "All decisions" }, { value: "allow", label: "Allowed" }, { value: "deny", label: "Denied" }]} onChange={e => change("decision", e.target.value)} data-testid="analytics-decision" />
        </div>
        {filters.scope === "tenant" ? <div className="analytics-team-filters">
          <TextField label="Find a team" name="analytics-team-search" autoComplete="off" type="search" value={teamSearch} onChange={e => setTeamSearch(e.target.value)} placeholder="Team name or slug…" data-testid="analytics-team-search" />
          <SelectField label="Team" value={filters.team_id || ""} options={[{ value: "", label: teamsQuery.isPending ? "Loading teams…" : "All teams" }, ...matchingTeams.map(team => ({ value: team.id, label: team.name || team.slug }))]} disabled={teamsQuery.isPending || !!teamsQuery.error} onChange={e => setFilters(current => ({ ...current, team_id: e.target.value, server: "", rpc_method: "", tool_name: "" }))} data-testid="analytics-team" />
        </div> : null}
        <div className="analytics-filter-actions"><Button variant="secondary" size="sm" icon="filter" aria-expanded={moreFilters} aria-controls={moreFilters ? "analytics-advanced-filters" : undefined} onClick={() => setMoreFilters(current => !current)} data-testid="analytics-more">More filters{advancedCount ? ` (${advancedCount})` : ""}</Button><Button variant="ghost" size="sm" onClick={reset} data-testid="analytics-clear">Reset filters</Button><span className="analytics-refresh-note"><span aria-hidden="true" /> Updates every {USAGE_REFRESH_MS / 1000} seconds</span></div>
        {moreFilters ? <div className="analytics-advanced-filters" id="analytics-advanced-filters">
          <SelectField label="MCP method" value={filters.rpc_method || ""} options={options(facets?.methods || [], "All methods", filters.rpc_method)} onChange={e => setFilters(current => ({ ...current, rpc_method: e.target.value, tool_name: "" }))} data-testid="analytics-method" />
          <SelectField label="Tool" value={filters.tool_name || ""} options={options(facets?.tools || [], "All tools", filters.tool_name)} disabled={!!filters.rpc_method && filters.rpc_method !== "tools/call"} onChange={e => setFilters(current => ({ ...current, tool_name: e.target.value, rpc_method: e.target.value ? "tools/call" : current.rpc_method }))} data-testid="analytics-tool" />
          <SelectField label="Policy version" value={filters.policy_version || ""} options={options(facets?.policy_versions || [], "All versions", filters.policy_version)} onChange={e => change("policy_version", e.target.value)} data-testid="analytics-policy" />
          <SelectField label="HTTP outcome" value={filters.http_status || ""} options={[{ value: "", label: "All outcomes" }, { value: "2xx", label: "2xx success" }, { value: "4xx", label: "4xx client errors" }, { value: "5xx", label: "5xx server errors" }]} onChange={e => change("http_status", e.target.value)} data-testid="analytics-http-status" />
        </div> : null}
      </FilterBar>
      {filters.scope === "tenant" && teamsQuery.error ? <p role="alert">Teams could not be loaded. Retry with Refresh.</p> : null}
    </section>
    <div className="analytics-context"><span>{timeLabel}{activeFilters ? ` with ${activeFilters} active ${activeFilters === 1 ? "filter" : "filters"}` : " across all scopes"}</span><span>Recorded gateway requests</span></div>
    <AsyncSection query={query} loadingLabel="Loading usage analytics…" errorTitle="Analytics is unavailable." onRetry={reload} onSignIn={onSignIn} testId="analytics">
      {usage ? <>
        <section aria-label="Gateway request summary"><dl className="analytics-kpis" data-testid="analytics-stats">
          {[
            { label: "Gateway requests", value: events?.toLocaleString(), detail: "All matching requests", tone: "default", icon: "activity" as const },
            { label: "Allowed", value: totals?.allowed?.toLocaleString(), detail: "Passed gateway policy", tone: "success", icon: "check" as const },
            { label: "Denied", value: totals?.denied?.toLocaleString(), detail: deniedRate === undefined ? "No request rate to calculate" : `${deniedRate}% of matching requests`, tone: totals?.denied ? "danger" : "default", icon: "shield" as const },
            { label: "p95 latency", value: ms(totals?.p95_latency_ms), detail: "Gateway-observed latency", tone: "default", icon: "clock" as const },
          ].map(metric => <div key={metric.label} className={`analytics-kpi analytics-kpi-${metric.tone}`}><dt><Icon name={metric.icon} />{metric.label}</dt><dd><strong>{metric.value ?? "—"}</strong><span>{metric.detail}</span></dd></div>)}
        </dl></section>
        <div className="analytics-insights">
          <div className="analytics-chart-panel">{usage.series?.length ? <UsageSeries points={usage.series} bucketSeconds={usage.bucket_seconds} /> : <><h2>Requests over time</h2><EmptyState icon="chart" title={events === 0 ? "No gateway requests in this range" : "Request trend is unavailable"} detail={events === 0 ? "Choose a larger time range or clear filters to look for activity." : "Totals are available, but the analytics service returned no time buckets."} /></>}</div>
          <section className="analytics-policy-panel" aria-labelledby="analytics-policy-title"><h2 id="analytics-policy-title">Policy outcomes</h2><p>Gateway decisions for the selected filters.</p>
            <div className="analytics-deny-rate"><strong>{deniedRate === undefined ? "—" : `${deniedRate}%`}</strong><span>deny rate</span></div>
            <div className="analytics-decision-track" role="img" aria-label={decisionRows.map(row => `${row.label}: ${row.value ?? "unknown"}`).join(", ")}>{events ? decisionRows.map(row => <span className={`decision-segment-${row.tone}`} key={row.label} style={{ width: `${Math.max(0, Math.min(100, (row.value ?? 0) / events * 100))}%` }} />) : null}</div>
            <dl className="analytics-decision-list">{decisionRows.filter(row => row.label !== "Other decisions" || row.value !== 0).map(row => <div key={row.label}><dt><span className={`decision-dot-${row.tone}`} aria-hidden="true" />{row.label}</dt><dd>{row.value?.toLocaleString() ?? "—"}</dd></div>)}</dl>
            <p className="analytics-policy-note">Allowed is a policy decision. It does not confirm that the tool succeeded.</p>
          </section>
        </div>
        <dl className="analytics-reliability" aria-label="Request reliability and usage" data-testid="analytics-reliability">{[
          { label: "HTTP errors", value: totals?.http_failures?.toLocaleString() }, { label: "Server errors", value: totals?.server_errors?.toLocaleString() }, { label: "Average latency", value: ms(totals?.average_latency_ms) }, { label: "Response traffic", value: bytes(totals?.bytes_out) },
          { label: "Active users", value: totals?.unique_humans?.toLocaleString() }, { label: "Active agents", value: totals?.unique_agents?.toLocaleString() }, { label: "Sessions", value: totals?.unique_sessions?.toLocaleString() }, { label: "Servers in window", value: totals?.unique_servers?.toLocaleString() },
        ].map(metric => <div key={metric.label}><dt>{metric.label}</dt><dd>{metric.value ?? "—"}</dd></div>)}</dl>
        <section className="analytics-requests-panel" aria-labelledby="analytics-requests-title">
          <div className="analytics-section-head"><div><h2 id="analytics-requests-title">Server requests</h2><p>Request groups ranked by volume. Open a trace to inspect the latest request.</p></div><SelectField label="Request groups" value={limit} options={[{ value: "10", label: "Top 10" }, { value: "25", label: "Top 25" }, { value: "50", label: "Top 50" }]} onChange={e => setLimit(e.target.value)} data-testid="analytics-limit" /></div>
          <DataTable columns={requestColumns} rows={usage.requests || []} rowKey={r => JSON.stringify([r.namespace, r.server, r.team_id, r.rpc_method, r.tool_name, r.decision, r.policy_version])} caption="Server requests by MCP method, tool and policy decision for the selected filters." regionLabel="Server request groups" testId="analytics-requests-table" rowTestId="analytics-request-row" minWidth={920} emptyMessage="No server requests match these filters." pageNote={`Top ${limit} request groups in this time range; totals and chart include all matching requests.`} />
        </section>
        <details className="analytics-callers-panel"><summary>Active callers <span>{usage.actors?.length ? `${usage.actors.length} loaded` : "No identified callers"}</span></summary><DataTable columns={actorColumns} rows={usage.actors || []} rowKey={r => `${r.human_id}/${r.agent_id}`} caption="Identified callers ranked by request volume in the selected filters." regionLabel="Actor usage" testId="analytics-actors-table" emptyMessage="No identified callers in this window." /></details>
        <details className="analytics-data-note"><summary>About this data</summary><p>Recorded gateway requests only. Servers without a gateway and requests whose analytics were not delivered are not included. HTTP errors include responses with status 400 or higher, including denials; server errors are HTTP 5xx. Active users count identified callers, not anonymous requests.</p></details>
      </> : null}
    </AsyncSection>
    <div className="analytics-audit"><GatewayAuditPanel /></div>
  </div>;
}
