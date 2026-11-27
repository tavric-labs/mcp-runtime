import { useQuery } from "@tanstack/react-query";
import { fetchJSON } from "../../api/client";
import type { TeamRecord } from "../../api/types";
import { StatusBadge } from "../../ui/Badge";
import { formatTimestamp } from "../../lib/format";

type NamespaceStatus = {
  health: string;
  reason: string;
  last_verified_at?: string;
};

export function TeamNamespaceStatus({ team }: { team: TeamRecord }) {
  const query = useQuery({
    queryKey: ["team-namespace", team.id],
    queryFn: async () => {
      const result = await fetchJSON(`/teams/${encodeURIComponent(team.slug)}`) as { team?: TeamRecord; status?: NamespaceStatus | null };
      if (result.team?.id !== team.id) throw new Error("Team status identity mismatch");
      return result.status ?? null;
    },
    retry: false,
    refetchInterval: 10000,
  });
  const status = query.data;
  const health = status?.health ?? (query.isPending ? "Pending" : "Unknown");
  return (
    <div data-testid="team-namespace-status">
      <p>Namespace health: <StatusBadge tone={health === "Ready" ? "ready" : health === "Degraded" ? "danger" : "unknown"}>{health}</StatusBadge></p>
      {status?.reason ? <p>{status.reason}</p> : null}
      <p>Last verified: {status?.last_verified_at ? formatTimestamp(status.last_verified_at) : "Not yet observed"}</p>
      {query.error ? <p>Namespace status is unavailable.</p> : null}
    </div>
  );
}
