import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { createGrant, getAgent, revokeGrantSessions, setGrantDisabled, setSessionRevoked } from "../../api/admin";
import { ForbiddenError, UnauthorizedError } from "../../api/client";
import type { AgentRecord, CertificateIssuance, GrantSummary, SessionSummary } from "../../api/types";
import { useAdminReload, useTeams, ADMIN_QUERY_KEY } from "../../hooks/useAdminData";
import { useCatalog } from "../../hooks/useCatalog";
import { formatTimestamp } from "../../lib/format";
import { StatusBadge } from "../../ui/Badge";
import { Button } from "../../ui/Button";
import { ConfirmDialog, type ConfirmRequest } from "../../ui/ConfirmDialog";
import { CopyButton } from "../../ui/CopyButton";
import { DataTable, buildColumns } from "../../ui/DataTable";
import { PageHeader } from "../../ui/PageHeader";
import { EmptyState, ErrorState, LoadingState } from "../../ui/States";
import { GrantForm, type GrantDraft } from "./accessForms";
import { sessionState } from "./AccessControlPanel";
import { AgentLifecycleDialog } from "./AgentLifecycleDialog";
import { AccessLink, AuthorizingGrant, revocationLabel } from "../access/accessLinks";
import { MetricGrid } from "../../ui/MetricCard";
import { AgentActivityPanel } from "./AgentActivityPanel";
import { Tabs } from "../../ui/Tabs";
import { Icon } from "../../ui/Icon";

const AGENT_VIEWS = ["access", "connect", "activity", "certificates"] as const;
type AgentView = typeof AGENT_VIEWS[number];
function agentView(value?: string): AgentView { return AGENT_VIEWS.includes(value as AgentView) ? value as AgentView : "access"; }
type Props = { id: string; platformAdmin: boolean; onBack: () => void; initialView?: string; onViewChange?: (view: string) => void };

function initialGrant(agent: AgentRecord, namespace: string): GrantDraft {
  return {
    name: "", namespace, server: "", humanID: "", agentID: agent.id,
    teamID: agent.team_id, maxTrust: "low", allowedSideEffects: ["read"], expiresAt: "",
  };
}

export function AgentDetailPanel({ id, platformAdmin, onBack, initialView, onViewChange }: Props) {
  const reload = useAdminReload();
  const query = useQuery({ queryKey: [ADMIN_QUERY_KEY, "agent", id], queryFn: () => getAgent(id) });
  const teamsQuery = useTeams(true);
  const teamNamespace = teamsQuery.data?.find((team) => team.slug === query.data?.agent.team_slug)?.namespace || "";
  const catalog = useCatalog(Boolean(query.data?.can_manage) && Boolean(teamNamespace), platformAdmin ? "" : teamNamespace);
  const [draft, setDraft] = useState<GrantDraft | null>(null);
  const [confirm, setConfirm] = useState<ConfirmRequest | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [lifecycle, setLifecycle] = useState(false);
  const [view, setView] = useState<AgentView>(() => agentView(initialView));
  useEffect(() => { setView(agentView(initialView)); }, [initialView, id]);
  function selectView(next: AgentView) { setView(next); onViewChange?.(next === "access" ? "" : next); }

  async function act(message: string, run: () => Promise<unknown>) {
    setBusy(true); setError(""); setNotice("");
    try { await run(); setNotice(message); setDraft(null); reload(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "The action failed."); }
    finally { setBusy(false); setConfirm(null); }
  }

  if (query.isPending) return <LoadingState label="Loading agent details…" testId="agent-detail-loading" />;
  if (query.error) return <ErrorState title={query.error instanceof ForbiddenError ? "You cannot view this agent." : query.error instanceof UnauthorizedError ? "Sign in to view this agent." : "Agent details could not be loaded."} detail={query.error instanceof Error ? query.error.message : ""} onRetry={() => void query.refetch()} testId="agent-detail-error" />;
  const { agent, grants, sessions, can_manage: canManage } = query.data;
  const canManageResource = (namespace: string) => canManage && (platformAdmin || namespace === teamNamespace);
  const grantColumns = buildColumns<GrantSummary>([
    { id: "name", header: "Grant", rowHeader: true, cell: (grant) => <AccessLink kind="grant" namespace={grant.namespace} name={grant.name} agent={agent.id} /> },
    { id: "server", header: "Server", cell: (grant) => grant.serverRef?.name || "—" },
    { id: "subject", header: "Human", cell: (grant) => grant.subject?.humanID || "Any team member" },
    { id: "trust", header: "Trust", cell: (grant) => grant.maxTrust || "—" },
    { id: "effects", header: "Side effects", cell: (grant) => grant.allowedSideEffects?.join(", ") || "—" },
    { id: "expires", header: "Expires", cell: (grant) => grant.expiresAt ? formatTimestamp(grant.expiresAt) : "No expiry" },
    { id: "status", header: "Status", cell: (grant) => <StatusBadge tone={grant.disabled ? "attention" : "ready"}>{grant.disabled ? "Disabled" : "Active"}</StatusBadge> },
    { id: "actions", header: "Actions", cell: (grant) => canManageResource(grant.namespace) ? <div className="cell-actions">
      <Button variant="ghost" size="sm" disabled={busy} onClick={() => setConfirm({ title: `${grant.disabled ? "Enable" : "Disable"} grant “${grant.name}”?`, body: "This changes whether new sessions can use the grant.", confirmLabel: grant.disabled ? "Enable grant" : "Disable grant", destructive: !grant.disabled, onConfirm: () => act(`Grant “${grant.name}” ${grant.disabled ? "enabled" : "disabled"}.`, () => setGrantDisabled(grant.namespace, grant.name, !grant.disabled)) })}>{grant.disabled ? "Enable" : "Disable"}</Button>
      <Button variant="ghost" size="sm" disabled={busy} onClick={() => setConfirm({ title: `Revoke sessions for grant “${grant.name}”?`, body: "Matching active sessions will be revoked.", confirmLabel: "Revoke sessions", destructive: true, onConfirm: () => act(`Sessions for grant “${grant.name}” revoked.`, () => revokeGrantSessions(grant.namespace, grant.name)) })}>Revoke sessions</Button>
    </div> : null },
  ]);
  const sessionColumns = buildColumns<SessionSummary>([
    { id: "name", header: "Session", rowHeader: true, cell: (session) => <AccessLink kind="session" namespace={session.namespace} name={session.name} agent={agent.id} /> },
    { id: "server", header: "Server", cell: (session) => session.serverRef?.name || "—" },
    { id: "human", header: "Human", cell: (session) => session.subject?.humanID || "—" },
    { id: "grant", header: "Authorizing grant", cell: (session) => <AuthorizingGrant session={session} /> },
    { id: "expires", header: "Expires", cell: (session) => session.expiresAt ? formatTimestamp(session.expiresAt) : "No expiry" },
    { id: "status", header: "Status", cell: (session) => { const state = sessionState(session); return <StatusBadge tone={state.tone}>{state.label}</StatusBadge>; } },
    { id: "cause", header: "Revocation cause", cell: revocationLabel },
    { id: "actions", header: "Actions", cell: (session) => canManageResource(session.namespace) && !session.revoked ? <Button variant="ghost" size="sm" disabled={busy} onClick={() => setConfirm({ title: `Revoke session “${session.name}”?`, body: "Future tool calls made with this session will be denied.", confirmLabel: "Revoke session", destructive: true, onConfirm: () => act(`Session “${session.name}” revoked.`, () => setSessionRevoked(session.namespace, session.name, true)) })}>Revoke</Button> : null },
  ]);
  const certificates = query.data.certificates;
  const certificateColumns = buildColumns<CertificateIssuance>([
    { id: "issued", header: "Issued", rowHeader: true, cell: row => formatTimestamp(row.issued_at) },
    { id: "session", header: "Session", cell: row => <AccessLink kind="session" name={row.session} namespace={row.namespace} agent={agent.id} /> },
    { id: "server", header: "Server", cell: row => `${row.namespace}/${row.server}` },
    { id: "human", header: "Delegating human", cell: row => row.human_id || "Unreported" },
    { id: "expires", header: "Certificate expires", cell: row => formatTimestamp(row.not_after) },
    { id: "serial", header: "Serial / fingerprint", cell: row => <><span className="copy-value">{row.serial}</span><span className="cell-detail copy-value">{row.fingerprint_sha256}</span></> },
  ]);

  const activeSessions = sessions.filter(session => sessionState(session).label === "Active").length;
  const owningTeam = teamsQuery.data?.find(team => team.slug === agent.team_slug);
  function openGrant() { selectView("access"); setDraft(initialGrant(agent, teamNamespace)); }
  const loginCommand = "mcp-runtime auth login --api-url <platform-url>";
  const proxyCommand = `mcp-runtime adapter proxy --runtime-url <https-mcp-url> --server <server> --namespace <namespace> --agent ${agent.id} --auto-refresh`;

  return <div className="agent-detail-page" data-testid="agent-detail">
    <PageHeader title={agent.name} description="Team-owned agent identity" breadcrumb={[{ label: "Agents", onClick: onBack }, { label: agent.name }]}
      actions={<><Button variant="secondary" icon="refresh" busy={query.isFetching} onClick={() => void query.refetch()}>Refresh agent</Button><Button variant="primary" icon="key" onClick={() => selectView("connect")}>Connect agent</Button></>} />
    <section className="agent-identity" aria-label="Agent identity">
      <div className="agent-identity-heading"><div className="agent-owner"><Icon name="shield" size={22} /><div><span className="agent-meta-label">Owning team</span><a href={`#/agents?team=${encodeURIComponent(agent.team_slug)}`}>{owningTeam?.name || agent.team_slug}</a></div></div><StatusBadge tone={agent.status === "active" ? "ready" : "attention"}>{agent.status}</StatusBadge></div>
      <div className="agent-stable-id"><span className="agent-meta-label">Agent ID</span><div><code translate="no">{agent.id}</code><CopyButton value={agent.id} label="Copy agent ID" /></div></div>
      <dl className="agent-metadata"><div><dt>Created by</dt><dd>{agent.created_by || "Not recorded"}</dd></div><div><dt>Created</dt><dd>{formatTimestamp(agent.created_at)}</dd></div><div><dt>Last updated</dt><dd>{formatTimestamp(agent.updated_at)}</dd></div></dl>
    </section>
    <div role="group" aria-label="Visible session lifecycle"><dl className="agent-summary">
      <div><dt>Applicable grants</dt><dd>{grants.length.toLocaleString()}</dd></div>
      <div><dt>Visible sessions issued</dt><dd>{sessions.length.toLocaleString()}</dd></div>
      <div><dt>Active sessions</dt><dd>{activeSessions.toLocaleString()}</dd></div>
      <div><dt>Certificates recorded <span>Last 7 days</span></dt><dd>{certificates?.recorded.toLocaleString() ?? "—"}</dd></div>
    </dl></div>
    <p className="agent-scope-caption">Access and session counts cover records visible to your account.</p>
    {notice ? <p className="notice notice-success" role="status">{notice}</p> : null}
    {error ? <p className="notice notice-danger" role="alert">{error}</p> : null}
    <Tabs items={[{ id: "access", label: "Access & sessions" }, { id: "connect", label: "Connect" }, { id: "activity", label: "Activity" }, { id: "certificates", label: "Certificates" }]} active={view} onSelect={selectView} label="Agent sections" testIdPrefix="agent-tab" />
    <div role="tabpanel" id="panel-access" aria-labelledby="tab-access" tabIndex={0} hidden={view !== "access"}>
      <section className="agent-panel" aria-labelledby="agent-grants-title">
        <div className="agent-section-head"><div><h2 id="agent-grants-title">Applicable grants</h2><p>Control which servers and tools this identity can use.</p></div>{canManage && agent.status === "active" && teamNamespace ? <Button variant="primary" icon="plus" onClick={openGrant} data-testid="agent-grant-access">Grant access</Button> : null}</div>
    {canManage && agent.status === "active" && teamsQuery.isPending ? <LoadingState label="Loading agent team…" testId="agent-team-loading" /> : null}
    {canManage && agent.status === "active" && teamsQuery.error ? <ErrorState title="Agent team could not be loaded." onRetry={() => void teamsQuery.refetch()} testId="agent-team-error" /> : null}
    {canManage && agent.status === "active" && teamNamespace ? <section className="agent-grant-form">
      {draft && catalog.status === "loading" ? <LoadingState label="Loading servers for grant…" testId="agent-grant-catalog-loading" /> : null}
      {draft && catalog.status === "error" ? <ErrorState title="Servers could not be loaded." detail={catalog.error} onRetry={catalog.reload} testId="agent-grant-catalog-error" /> : null}
      {draft && catalog.status === "unauthorized" ? <ErrorState title="Sign in to load servers." testId="agent-grant-catalog-unauthorized" /> : null}
      {draft && catalog.status === "ready" ? <GrantForm draft={draft} fixedAgent={{ id: agent.id, teamID: agent.team_id, teamSlug: agent.team_slug }} servers={catalog.servers} namespaces={catalog.namespaces.map((item) => item.namespace)} busy={busy} submitError={error} onChange={setDraft} onCancel={() => setDraft(null)} onSubmit={() => void act(`Grant “${draft.name}” created.`, () => createGrant({ name: draft.name.trim(), namespace: draft.namespace, serverRef: { name: draft.server.trim() }, subject: { agentID: agent.id, teamID: agent.team_id }, mode: draft.mode || "enforce", maxTrust: draft.maxTrust, allowedSideEffects: draft.allowedSideEffects, expiresAt: draft.expiresAt ? new Date(draft.expiresAt).toISOString() : undefined }))} /> : null}
    </section> : null}

        {grants.length ? <DataTable columns={grantColumns} rows={grants} rowKey={grant => `${grant.namespace}/${grant.name}`} caption="Grants applicable to this agent and visible to you." regionLabel="Agent grants" testId="agent-grants-table" emptyMessage="No applicable grants." /> : <EmptyState icon="shield" title="No access grants yet" detail={canManage ? "Create a grant for a target server to allow this agent to request a session." : "A team owner or platform administrator can grant this agent access to a server."} testId="agent-grants-empty" />}
      </section>
      <section className="agent-panel" aria-labelledby="agent-sessions-title">
        <div className="agent-section-head"><div><h2 id="agent-sessions-title">Sessions</h2><p>Connections issued for this agent, including revoked and expired history.</p></div></div>
        {sessions.length ? <DataTable columns={sessionColumns} rows={sessions} rowKey={session => `${session.namespace}/${session.name}`} caption="Agent sessions visible to you, including expired and revoked history." regionLabel="Agent sessions" testId="agent-sessions-table" emptyMessage="No sessions visible." /> : <EmptyState icon="key" title="No sessions yet" detail="Connect an MCP client through the adapter to create an authorized session for this identity." action={<Button variant="secondary" onClick={() => selectView("connect")}>View connection steps</Button>} testId="agent-sessions-empty" />}
      </section>
    </div>
    <div role="tabpanel" id="panel-connect" aria-labelledby="tab-connect" tabIndex={0} hidden={view !== "connect"}>
      <section className="agent-panel agent-connect" aria-labelledby="agent-connect-title"><h2 id="agent-connect-title">Connect this agent</h2><p className="agent-section-description">Use this identity with your local MCP client. Start with a server grant, then sign in and run the adapter.</p>
        {agent.status !== "active" ? <p className="notice notice-danger">This agent is inactive. A team owner or platform administrator must reactivate it before new connections can be created.</p> : null}
        <ol className="agent-connection-steps">
          <li><div className="agent-step-content"><h3>Grant access to a server</h3><p>Use an enabled grant for this agent and your target server. A team owner or platform administrator can create one.</p>{canManage && agent.status === "active" && teamNamespace ? <Button variant="secondary" size="sm" onClick={openGrant}>Choose a server and grant access</Button> : null}</div></li>
          <li><div className="agent-step-content"><h3>Sign in to your platform</h3><p>Replace the platform URL with your installation’s address.</p><div className="agent-command"><pre><code translate="no">{loginCommand}</code></pre><CopyButton value={loginCommand} label="Copy sign-in command" /></div></div></li>
          <li><div className="agent-step-content"><h3>Start the local adapter</h3><p>Replace the MCP URL, server, and namespace. This agent ID is already filled in.</p><div className="agent-command"><pre><code translate="no">{proxyCommand}</code></pre><CopyButton value={proxyCommand} label="Copy adapter command" /></div><p className="agent-command-note">The adapter creates an authorized session and enrolls a certificate in memory.</p></div></li>
        </ol>
        <details className="agent-help"><summary>Saved certificates and OAuth targets</summary><p>For a saved certificate, use <code>mcp-runtime adapter enroll</code> with the same server, namespace, and agent. The private key stays local. OAuth targets also require a bearer token from the MCP client. A grant alone does not establish a connection; direct clients follow the server’s gateway policy.</p></details>
      </section>
    </div>
    <div role="tabpanel" id="panel-activity" aria-labelledby="tab-activity" tabIndex={0} hidden={view !== "activity"}>{view === "activity" ? <AgentActivityPanel key={agent.id} agentID={agent.id} platformAdmin={platformAdmin} /> : null}</div>
    <div role="tabpanel" id="panel-certificates" aria-labelledby="tab-certificates" tabIndex={0} hidden={view !== "certificates"}>
    <section className="agent-panel" aria-label="Agent certificate issuance">
      <h2 className="section-title">Recorded certificate issuance</h2>
      <p className="section-note">Public certificate issuance recorded in the last 7 days.</p>
      <details className="agent-help"><summary>About certificate history</summary><p>Certificates are issued to a human-delegated session for this agent. A session can have multiple certificates. Recording starts with v0.12.0; older or unrecorded issuance is unknown. Expired or revoked sessions invalidate access even if a certificate has time remaining.</p></details>
      {certificates ? <>
        <MetricGrid label="Recorded certificates" metrics={[{ label: "Recorded certificates issued", value: certificates.recorded, hint: "Last 7 days, within your agent visibility" }]} />
        <p className="section-note">{formatTimestamp(certificates.since)} – {formatTimestamp(certificates.until)}. Showing the latest {certificates.recent.length} of {certificates.recorded} recorded issuances (maximum 100).</p>
        <DataTable columns={certificateColumns} rows={certificates.recent} rowKey={row => `${row.fingerprint_sha256}/${row.issued_at}`} caption="Recorded certificate issuance; private keys and certificate PEM are never displayed." regionLabel="Agent certificate history" testId="agent-certificates-table" emptyMessage="No certificate issuance recorded in this window. This does not establish that no older certificates exist." />
      </> : <ErrorState title="Certificate history is unavailable." detail={query.data.certificates_error || "The service did not return recorded issuance data."} onRetry={() => void query.refetch()} testId="agent-certificates-error" />}
    </section>
    </div>
    {canManage ? <details className="agent-maintenance"><summary>Agent lifecycle</summary><div><h2>{agent.status === "active" ? "Deactivate this identity" : "Reactivate this identity"}</h2><p>{agent.status === "active" ? "Deactivation revokes this agent’s sessions. Review the affected sessions before confirming." : "Reactivation allows new sessions. Previously revoked sessions stay revoked."}</p><Button variant={agent.status === "active" ? "danger-outline" : "secondary"} onClick={() => setLifecycle(true)}>{agent.status === "active" ? "Deactivate agent" : "Reactivate agent"}</Button></div></details> : null}
    {confirm ? <ConfirmDialog {...confirm} busy={busy} onCancel={() => setConfirm(null)} /> : null}
    {lifecycle ? <AgentLifecycleDialog agent={agent} onClose={() => setLifecycle(false)} onChanged={reload} /> : null}
  </div>;
}
