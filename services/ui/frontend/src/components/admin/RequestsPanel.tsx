import { grafanaPath } from "../../api/config";
import { GatewayAuditPanel } from "./GatewayAuditPanel";
import { TracesPanel } from "./TracesPanel";
import { UsageAnalyticsPanel } from "./UsageAnalyticsPanel";
import { Button, ButtonLink } from "../../ui/Button";
import { PageHeader } from "../../ui/PageHeader";
import { useAdminReload } from "../../hooks/useAdminData";

type RequestsPanelProps = {
  onSignIn: () => void;
};

// One page for what passed through the platform: gateway usage, each traced
// request, and the policy decisions behind them. Sections collapse so an admin
// can focus on one without losing the others' filters.
export function RequestsPanel({ onSignIn }: RequestsPanelProps) {
  const reload = useAdminReload();
  return (
    <div className="analytics-page requests-page">
      <PageHeader
        title="Requests"
        breadcrumb={[{ label: "Administration" }, { label: "Requests" }]}
        description="Gateway usage, every traced request, and policy decisions in one place."
        actions={
          <>
            <ButtonLink variant="secondary" href={`${grafanaPath()}/explore`} target="_blank" rel="noreferrer" trailingIcon="external" data-testid="analytics-detailed-activity">
              Detailed activity
            </ButtonLink>
            <Button variant="secondary" icon="refresh" onClick={reload} data-testid="requests-refresh">
              Refresh
            </Button>
          </>
        }
      />
      <details className="analytics-callers-panel requests-section" open data-testid="requests-section-summary">
        <summary>
          Summary <span>Gateway usage, policy outcomes and reliability</span>
        </summary>
        <UsageAnalyticsPanel onSignIn={onSignIn} embedded />
      </details>
      <details className="analytics-callers-panel requests-section" open data-testid="requests-section-recent">
        <summary>
          Recent requests <span>Tool calls and platform actions, each with its trace</span>
        </summary>
        <TracesPanel onSignIn={onSignIn} />
      </details>
      <details className="analytics-callers-panel requests-section" data-testid="requests-section-decisions">
        <summary>
          Gateway decisions <span>Recorded decisions with the grant and session that matched</span>
        </summary>
        <GatewayAuditPanel />
      </details>
    </div>
  );
}
