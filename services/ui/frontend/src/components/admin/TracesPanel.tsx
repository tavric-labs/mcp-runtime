import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { AsyncSection } from "./AsyncSection";
import { traceExploreURL } from "./UsageAnalyticsPanel";
import { listEvents, readOperations } from "../../api/admin";
import { StatusBadge, outcomeTone } from "../../ui/Badge";
import { Button, ButtonLink } from "../../ui/Button";
import { DataTable, buildColumns } from "../../ui/DataTable";
import { TextField } from "../../ui/Field";
import { FilterBar, FilterSummary, type FilterChip } from "../../ui/FilterBar";
import { formatAbsolute, formatTimestamp } from "../../lib/format";
import {
  TRACE_KIND_LABEL,
  isTraceID,
  mergeTraceActivity,
  type TraceActivityRow,
} from "../../lib/traceActivity";
import { ADMIN_QUERY_KEY, OPERATIONS_LIMIT, useAdminReload } from "../../hooks/useAdminData";

type TracesPanelProps = {
  onSignIn: () => void;
};

const LOADED_NOTE = `loaded entries (the API returns at most ${OPERATIONS_LIMIT} per source)`;

// A section of the Requests page: the page owns the header and Refresh.
export function TracesPanel({ onSignIn }: TracesPanelProps) {
  const [draft, setDraft] = useState("");
  const [traceId, setTraceId] = useState("");
  const [invalid, setInvalid] = useState(false);
  const reload = useAdminReload();

  // Platform operations (console, CLI and API actions) come from platform-api's
  // audit trail; tool calls come from the gateway events in analytics. Each is
  // its own query so an unavailable analytics path never hides platform rows.
  const platform = useQuery({
    queryKey: [ADMIN_QUERY_KEY, "traces", "platform", traceId],
    queryFn: () => readOperations({ trace_id: traceId, limit: OPERATIONS_LIMIT }),
  });
  const gateway = useQuery({
    queryKey: [ADMIN_QUERY_KEY, "traces", "gateway", traceId],
    queryFn: () => listEvents({ limit: OPERATIONS_LIMIT, ...(traceId ? { trace_id: traceId } : {}) }),
  });

  const rows = useMemo(
    () => mergeTraceActivity(platform.data?.audit_logs ?? [], gateway.data ?? []),
    [platform.data, gateway.data]
  );

  const columns = useMemo(
    () =>
      buildColumns<TraceActivityRow>([
        {
          id: "time",
          header: "Time",
          rowHeader: true,
          sortValue: (row) => Date.parse(row.time) || 0,
          cell: (row) => <span title={formatAbsolute(row.time)}>{formatTimestamp(row.time)}</span>,
        },
        {
          id: "kind",
          header: "Type",
          sortValue: (row) => row.kind,
          cell: (row) => <StatusBadge tone="neutral">{TRACE_KIND_LABEL[row.kind]}</StatusBadge>,
        },
        { id: "operation", header: "Operation", sortValue: (row) => row.operation, cell: (row) => row.operation },
        {
          id: "target",
          header: "Target",
          sortValue: (row) => row.target,
          cell: (row) => <span className="cell-code wrap-anywhere">{row.target}</span>,
        },
        { id: "actor", header: "Actor", sortValue: (row) => row.actor, cell: (row) => row.actor },
        {
          id: "outcome",
          header: "Outcome",
          sortValue: (row) => row.outcome,
          cell: (row) => <StatusBadge tone={outcomeTone(row.outcome)}>{row.outcome}</StatusBadge>,
        },
        {
          id: "trace",
          header: "Trace",
          cell: (row) => {
            if (!isTraceID(row.traceId)) return <span title="No trace was recorded for this entry">—</span>;
            const explore = traceExploreURL(row.traceId);
            return (
              <span className="cell-actions">
                <Button
                  variant="ghost"
                  size="sm"
                  data-testid="trace-follow"
                  title={`Show every operation in trace ${row.traceId}`}
                  onClick={() => follow(row.traceId)}
                >
                  <span className="cell-code">{row.traceId.slice(0, 8)}</span>
                </Button>
                {explore ? (
                  <ButtonLink
                    variant="ghost"
                    size="sm"
                    href={explore}
                    target="_blank"
                    rel="noreferrer"
                    trailingIcon="external"
                    data-testid="trace-open"
                  >
                    Open trace
                  </ButtonLink>
                ) : null}
              </span>
            );
          },
        },
      ]),
    // follow only calls state setters, which are stable.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    []
  );

  function follow(id: string) {
    setDraft(id);
    setInvalid(false);
    setTraceId(id);
  }

  function apply() {
    const value = draft.trim().toLowerCase();
    if (value && !isTraceID(value)) {
      setInvalid(true);
      return;
    }
    setInvalid(false);
    setTraceId(value);
  }

  function clear() {
    setDraft("");
    setInvalid(false);
    setTraceId("");
  }

  const chips: FilterChip[] = traceId ? [{ id: "trace", label: "Trace", value: traceId, onRemove: clear }] : [];
  const gatewayUnavailable = Boolean(gateway.error) && !gateway.isPending;
  const traceLink = traceId ? traceExploreURL(traceId) : undefined;

  return (
    <>
      <FilterBar label="Filter traces" onSubmit={apply}>
        <TextField
          label="Trace id"
          type="search"
          fieldClassName="grow"
          leadingIcon
          placeholder="32 hex digits, e.g. 4bf92f3577b34da6a3ce929d0e0e4736"
          value={draft}
          data-testid="traces-trace-filter"
          error={invalid ? "A trace id is 32 lowercase hex digits." : undefined}
          onChange={(event) => setDraft(event.target.value)}
        />
        <Button type="submit" variant="primary" data-testid="traces-apply">
          Apply
        </Button>
        {traceLink ? (
          <ButtonLink variant="secondary" href={traceLink} target="_blank" rel="noreferrer" trailingIcon="external" data-testid="traces-open-tempo">
            Open in Tempo
          </ButtonLink>
        ) : null}
      </FilterBar>

      <FilterSummary chips={chips} count={`${rows.length} operations loaded`} onClear={clear} testId="traces-summary" />

      {gatewayUnavailable ? (
        <p className="section-note" role="status" data-testid="traces-gateway-unavailable">
          Tool calls could not be loaded, so only platform operations are shown. The gateway analytics path may be unavailable.
        </p>
      ) : null}

      <AsyncSection
        query={platform}
        loadingLabel="Loading traced operations…"
        errorTitle="Traced operations could not be loaded."
        onRetry={reload}
        onSignIn={onSignIn}
        testId="traces"
      >
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(row) => row.key}
          caption="Traced platform operations and tool calls with their trace ids."
          regionLabel="Request traces"
          testId="traces-table"
          emptyMessage={traceId ? "Nothing was recorded under this trace id." : "No traced operations recorded yet."}
          pageNote={LOADED_NOTE}
        />
      </AsyncSection>
    </>
  );
}
