import { useQuery } from "@tanstack/react-query";
import { readOverviewDirectory, readOverviewServers } from "../../api/workspace";
import { readOperations } from "../../api/admin";
import { useAdminReload } from "../../hooks/useAdminData";
import { formatTimestamp } from "../../lib/format";
import { AsyncSection } from "./AsyncSection";
import { PlatformHealthPanel } from "./PlatformHealthPanel";
import { StatusBadge, outcomeTone } from "../../ui/Badge";
import { Button, ButtonLink } from "../../ui/Button";
import { MetricGrid } from "../../ui/MetricCard";
import { PageHeader } from "../../ui/PageHeader";
import { Icon } from "../../ui/Icon";

export function PlatformOverview({ onSignIn }: { onSignIn: () => void }) {
  const directory = useQuery({ queryKey: ["admin", "overview", "directory"], queryFn: readOverviewDirectory });
  const servers = useQuery({ queryKey: ["admin", "overview", "servers"], queryFn: readOverviewServers });
  const operations = useQuery({ queryKey: ["admin", "overview", "audit"], queryFn: () => readOperations({ limit: "8" }) });
  const reload = useAdminReload();
  const totals = directory.isError ? undefined : directory.data;
  const fleet = servers.isError ? undefined : servers.data;

  return <div className="platform-overview">
    <PageHeader title="Platform overview" breadcrumb={[{ label: "Administration" }, { label: "Overview" }]}
      description="Resources, deployment issues, and service health across the platform."
      actions={<Button variant="secondary" icon="refresh" onClick={reload} busy={directory.isFetching || servers.isFetching || operations.isFetching} data-testid="platform-refresh">Refresh overview</Button>} />
    <section className="section" aria-labelledby="overview-resources-title">
      <div className="overview-section-head"><div><h2 id="overview-resources-title">Platform resources</h2><p className="section-note">Platform-wide totals. Your memberships are in <a href="#/workspace">My workspace</a>.</p></div></div>
      <MetricGrid label="Platform-wide totals" metrics={[
        { label: "Teams", value: totals?.teams, icon: "users" },
        { label: "Users", value: totals?.users, icon: "user" },
        { label: "Agent identities", value: totals?.agents, icon: "shield" },
        { label: "Active sessions", value: totals?.active_sessions, icon: "key" },
        { label: "Servers", value: fleet?.servers, icon: "server" },
      ]} />
      <AsyncSection query={directory} loadingLabel="Loading directory totals…" errorTitle="Directory totals are unavailable." onRetry={() => void directory.refetch()} onSignIn={onSignIn} testId="overview-directory">
        <div className="inline-actions">
          <ButtonLink size="sm" href="#/admin/teams">Manage teams</ButtonLink>
          <ButtonLink size="sm" href="#/agents">Manage agent identities</ButtonLink>
          <ButtonLink size="sm" href="#/access">Manage access</ButtonLink>
          <ButtonLink size="sm" href="#/servers">Open server catalog</ButtonLink>
        </div>
      </AsyncSection>
      <details className="overview-count-details"><summary>What these totals include</summary><p>Non-deleted teams and users, and all agents of current teams. Active sessions exclude revoked, expired, retired-server and inactive-agent sessions.</p></details>
    </section>
    <section className="section" aria-labelledby="overview-deployments-title">
      <h2 id="overview-deployments-title">Deployment issues</h2>
      <AsyncSection query={servers} loadingLabel="Loading server health…" errorTitle="Server totals and deployment issues are unavailable." onRetry={() => void servers.refetch()} onSignIn={onSignIn} testId="overview-servers">
        {fleet?.failures.length ? <ul className="overview-issue-list">{fleet.failures.map(item => <li key={`${item.namespace}/${item.name}`}>
          <div><Icon name="alert" /><a href={`#/servers?namespace=${encodeURIComponent(item.namespace)}&server=${encodeURIComponent(`${item.namespace}/${item.name}`)}`}>{item.namespace}/{item.name}</a></div>
          <StatusBadge tone="attention">{item.status || "Unknown"}</StatusBadge>
        </li>)}</ul> : <p className="overview-clear"><Icon name="check" /> No reported deployment failures.</p>}
      </AsyncSection>
    </section>
    <section className="section" aria-labelledby="overview-health-title"><PlatformHealthPanel onSignIn={onSignIn} /></section>
    <section className="section" aria-labelledby="overview-audit-title">
      <div className="overview-section-head"><div><h2 id="overview-audit-title">Recent administrative activity</h2><p className="section-note">The latest eight audit records from across the platform.</p></div><ButtonLink size="sm" href="#/admin/operations">View audit trail</ButtonLink></div>
      <AsyncSection query={operations} loadingLabel="Loading audit changes…" errorTitle="Audit changes are unavailable." onRetry={() => void operations.refetch()} onSignIn={onSignIn} testId="overview-audit">
        {operations.data?.audit_logs.length ? <ul className="overview-audit-list">{operations.data.audit_logs.slice(0, 8).map((item, index) => <li key={`${item.created_at}:${index}`}>
          <div><strong>{item.action}</strong><span>{item.resource}</span></div>
          <time dateTime={item.created_at}>{formatTimestamp(item.created_at)}</time>
          <StatusBadge tone={outcomeTone(item.status)}>{item.status || "Unknown"}</StatusBadge>
        </li>)}</ul> : <p className="section-note">No recent administrative audit records.</p>}
      </AsyncSection>
    </section>
  </div>;
}
