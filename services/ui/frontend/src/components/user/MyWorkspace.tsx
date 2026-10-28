import { useQuery } from "@tanstack/react-query";
import { listTeamAgents } from "../../api/admin";
import { readWorkspaceAccess, readWorkspaceServers, readWorkspaceTeams } from "../../api/workspace";
import { isAdmin, type AuthStatus, type TeamRecord } from "../../api/types";
import { readUserUsage } from "../../api/userWorkflows";
import { AsyncSection } from "../admin/AsyncSection";
import { TeamsPanel } from "../admin/TeamsPanel";
import { sessionState } from "../admin/AccessControlPanel";
import { AccessLink } from "../access/accessLinks";
import { PageHeader } from "../../ui/PageHeader";
import { Button, ButtonLink } from "../../ui/Button";
import { StatusBadge } from "../../ui/Badge";
import { Icon } from "../../ui/Icon";
import { EmptyState } from "../../ui/States";
import { RecentActivityTable } from "../usage/UsageTables";
import { expiryState, formatTimestamp } from "../../lib/format";

type Props = { auth: AuthStatus; onSignIn: () => void; section?: string; team?: string };

function TeamAgents({ team, admin, onSignIn }: { team: TeamRecord; admin: boolean; onSignIn: () => void }) {
  const query = useQuery({ queryKey: ["workspace", "agents", team.slug], queryFn: () => listTeamAgents(team.slug, { limit: "5" }) });
  const canManage = admin || team.role === "owner";
  return <section className="workspace-team" aria-label={`${team.name} team`}>
    <header className="workspace-team-head">
      <div className="workspace-team-identity"><span className="workspace-team-icon"><Icon name="users" size={22} /></span><div><h3>{team.name}</h3><p className="workspace-namespace">{team.namespace}</p></div></div>
      <StatusBadge tone={team.role === "owner" ? "info" : "neutral"} dot={false}>{`Your role: ${team.role || "unknown"}`}</StatusBadge>
    </header>
    <div className="workspace-team-body">
      <p className="workspace-list-label">Team-owned agents</p>
      <AsyncSection query={query} loadingLabel="Loading team agents…" errorTitle="Agents are unavailable." onRetry={() => void query.refetch()} onSignIn={onSignIn} testId={`workspace-agents-${team.slug}`}>
        {query.data?.agents.length ? <ul className="workspace-agent-list">{query.data.agents.map(agent => <li key={agent.id}>
          <a href={`#/agents?team=${encodeURIComponent(team.slug)}&agent=${encodeURIComponent(agent.id)}`}><span className="workspace-agent-name"><Icon name="shield" /><span>{agent.name}</span></span><StatusBadge tone={agent.status === "active" ? "ready" : "neutral"}>{agent.status || "Unknown"}</StatusBadge><Icon name="chevronRight" /></a>
        </li>)}</ul> : <p className="workspace-muted">No agents yet. {canManage ? "Open the agent directory to create a team identity." : "A team owner can create an agent identity."}</p>}
        {query.data?.next_cursor ? <p className="workspace-muted">Showing the first 5 agents. Open the directory to see more.</p> : null}
      </AsyncSection>
    </div>
    <footer className="workspace-team-foot"><ButtonLink size="sm" variant="secondary" href={`#/agents?team=${encodeURIComponent(team.slug)}`}>View agents</ButtonLink>{canManage ? <ButtonLink size="sm" variant="ghost" href={`#/workspace/team?team=${encodeURIComponent(team.slug)}`}>Manage members</ButtonLink> : <span className="workspace-muted">Members can view permitted agents.</span>}</footer>
  </section>;
}

export function MyWorkspace({ auth, onSignIn, section, team }: Props) {
  const enabled = auth.authenticated;
  const admin = isAdmin(auth);
  const teams = useQuery({ queryKey: ["workspace", "teams"], queryFn: readWorkspaceTeams, enabled });
  const servers = useQuery({ queryKey: ["workspace", "servers"], queryFn: readWorkspaceServers, enabled: enabled && section !== "team" });
  const access = useQuery({ queryKey: ["workspace", "access"], queryFn: readWorkspaceAccess, enabled: enabled && section !== "team" });
  const activity = useQuery({ queryKey: ["workspace", "activity"], queryFn: () => readUserUsage(7), enabled: enabled && !admin && section !== "team" });
  if (!enabled) return <EmptyState title="Sign in to see My workspace." action={<Button variant="primary" onClick={onSignIn}>Sign in</Button>} />;

  if (section === "team") {
    const manageable = (teams.data ?? []).filter(item => admin || item.role === "owner");
    return <><PageHeader title="Team management" description="Membership management for teams you administer." />
      <a href="#/workspace">Back to My workspace</a>
      <AsyncSection query={teams} loadingLabel="Checking team management access…" errorTitle="Teams are unavailable." onRetry={() => void teams.refetch()} onSignIn={onSignIn} testId="workspace-management">
        {manageable.some(item => item.slug === team) ? <TeamsPanel onSignIn={onSignIn} managedTeams={manageable} initialTeam={team} /> : <EmptyState title="You cannot manage this team." detail="Team ownership or platform administrator authority is required." />}
      </AsyncSection></>;
  }
  const rows = servers.data?.servers ?? [];
  const summary = [
    { label: "My teams", value: teams.isSuccess ? teams.data.length : undefined },
    { label: "Accessible servers", value: servers.isSuccess ? rows.length : undefined },
    { label: "Manageable servers", value: servers.isSuccess ? rows.filter(item => item.can_manage).length : undefined },
    { label: "Access grants", value: access.isSuccess ? access.data.grants.length : undefined },
  ];
  return <div className="workspace-home">
    <PageHeader title="My workspace" description="Your teams, agent identities, and access in one place."
      actions={admin ? <ButtonLink variant="secondary" icon="gauge" href="#/admin/overview">Platform overview</ButtonLink> : <ButtonLink variant="secondary" icon="activity" href="#/activity">View activity</ButtonLink>} />
    <dl className="workspace-summary" aria-label="Your workspace summary">{summary.map(item => <div key={item.label}><dt>{item.label}</dt><dd>{item.value === undefined ? "—" : item.value.toLocaleString()}</dd></div>)}</dl>
    <div className="workspace-columns">
      <section className="workspace-teams-section" aria-labelledby="workspace-teams-title">
        <div className="workspace-section-head"><div><h2 id="workspace-teams-title">My teams</h2><p>Memberships and the agents your teams own.</p></div>{admin ? <a href="#/admin/teams">Manage platform teams</a> : null}</div>
        <AsyncSection query={teams} loadingLabel="Loading your memberships…" errorTitle="Memberships are unavailable." onRetry={() => void teams.refetch()} onSignIn={onSignIn} testId="workspace-teams">
          {teams.data?.length ? <div className="workspace-team-list">{teams.data.map(item => <TeamAgents key={item.id} team={item} admin={admin} onSignIn={onSignIn} />)}</div> : <EmptyState icon="users" title="No team memberships yet" detail={admin ? "Create or manage teams from platform administration. Memberships appear here once you join a team." : "Ask a team owner to add you. Your team’s agents will appear here."} />}
        </AsyncSection>
      </section>
      <div className="workspace-resource-column">
        <section className="workspace-panel" aria-labelledby="workspace-servers-title">
          <div className="workspace-section-head"><h2 id="workspace-servers-title">Servers</h2><Icon name="server" /></div>
          <AsyncSection query={servers} loadingLabel="Loading permitted servers…" errorTitle="Servers are unavailable." onRetry={() => void servers.refetch()} onSignIn={onSignIn} testId="workspace-servers">
            <div className="workspace-server-counts"><span>Accessible to me: {rows.length}</span><span>Manageable by me: {rows.filter(item => item.can_manage).length}</span></div>
            {rows.length ? <ul className="workspace-resource-list">{rows.slice(0, 4).map(item => <li key={`${item.namespace}/${item.name}`}><a href={`#/servers?namespace=${encodeURIComponent(item.namespace)}&server=${encodeURIComponent(`${item.namespace}/${item.name}`)}`}><strong>{item.name}</strong><span>{item.namespace}</span></a><StatusBadge tone={item.status === "Ready" ? "ready" : "neutral"}>{item.status || "Unknown"}</StatusBadge></li>)}</ul> : <div className="workspace-compact-empty"><Icon name="server" size={24} /><h3>No accessible servers</h3><p>Browse the catalog to find servers available to you.</p></div>}
            {rows.length > 4 ? <p className="workspace-muted">Showing 4 of {rows.length} accessible servers.</p> : null}
          </AsyncSection>
          <ButtonLink size="sm" variant="secondary" href="#/servers">Open server catalog</ButtonLink>
        </section>
        <section className="workspace-panel" aria-labelledby="workspace-access-title">
          <div className="workspace-section-head"><h2 id="workspace-access-title">My access</h2><Icon name="key" /></div>
          <p className="workspace-muted">Your human and team grants, and your personal sessions.</p>
          <AsyncSection query={access} loadingLabel="Loading your access…" errorTitle="Access records are unavailable." onRetry={() => void access.refetch()} onSignIn={onSignIn} testId="workspace-access">
            {!access.data?.grants.length && !access.data?.sessions.length ? <div className="workspace-compact-empty"><Icon name="shield" size={24} /><h3>No personal grants or sessions</h3><p>Granted access and sessions will appear here. Check agent details for agent-specific access.</p></div> : <ul className="workspace-access-list">
              {access.data?.grants.slice(0, 4).map(item => { const expiry = expiryState(item.expiresAt); const label = item.disabled ? "Disabled" : expiry === "expired" ? "Expired" : expiry === "unparseable" ? "Unknown expiry" : "Enabled"; return <li key={`grant:${item.namespace}/${item.name}`}><div><span className="workspace-list-label">Grant</span><AccessLink kind="grant" namespace={item.namespace} name={item.name} /><span className="workspace-muted">{item.expiresAt ? `Expires ${formatTimestamp(item.expiresAt)}` : "No expiry"}</span></div><StatusBadge tone={label === "Enabled" ? "ready" : "neutral"}>{label}</StatusBadge></li>; })}
              {access.data?.sessions.slice(0, 4).map(item => { const state = sessionState(item); return <li key={`session:${item.namespace}/${item.name}`}><div><span className="workspace-list-label">Session</span><AccessLink kind="session" namespace={item.namespace} name={item.name} /><span className="workspace-muted">{item.expiresAt ? `Expires ${formatTimestamp(item.expiresAt)}` : "No expiry"}</span></div><StatusBadge tone={state.tone}>{state.label}</StatusBadge></li>; })}
            </ul>}
            {(access.data?.grants.length ?? 0) > 4 || (access.data?.sessions.length ?? 0) > 4 ? <p className="workspace-muted">Showing up to 4 grants and 4 sessions. Open Access control for all records.</p> : null}
          </AsyncSection>
          <ButtonLink size="sm" variant="secondary" href="#/access">Open Access control</ButtonLink>
        </section>
      </div>
    </div>
    <section className="workspace-panel workspace-activity" aria-labelledby="workspace-activity-title">
      <div className="workspace-section-head"><div><h2 id="workspace-activity-title">{admin ? "Platform activity" : "Recent activity"}</h2><p>{admin ? "Inspect platform-wide gateway usage and decisions." : "Gateway activity across your namespaces in the last 7 days."}</p></div><ButtonLink size="sm" href={admin ? "#/admin/requests" : "#/activity"}>{admin ? "Open Requests" : "View namespace activity"}</ButtonLink></div>
      {!admin ? <AsyncSection query={activity} loadingLabel="Loading namespace activity…" errorTitle="Namespace activity is unavailable." onRetry={() => void activity.refetch()} onSignIn={onSignIn} testId="workspace-activity"><RecentActivityTable rows={activity.data?.recent ?? []} /></AsyncSection> : null}
    </section>
    <details className="workspace-scope-note"><summary>How workspace access works</summary><p>Teams shown here are your actual memberships. Administrator authority is platform-wide and does not create memberships. Accessible servers are visible in your catalog; tool calls still require the applicable grants and session policy. Management authority is checked separately by the API.</p><p>Deployed by me: unavailable. Deployment actors are not recorded in the server API; ownership and visibility do not establish historical attribution. Namespace activity may include other team members’ actions.</p></details>
  </div>;
}
