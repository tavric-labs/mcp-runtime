import { useQuery } from "@tanstack/react-query";

import { fetchJSON } from "../api/client";
import type { Operation } from "../api/types";
import { CopyField } from "./CopyButton";
import { StatusBadge } from "./Badge";

const terminal = new Set(["Succeeded", "Failed", "Superseded"]);
const states = new Set(["Pending", "Running", ...terminal]);

export function OperationStatus({ operation }: { operation: Operation }) {
  const query = useQuery({
    queryKey: ["operation", operation.id],
    initialData: operation,
    queryFn: async () => {
      const result = await fetchJSON(`/operations/${encodeURIComponent(operation.id)}`) as { operation?: Operation };
      if (result.operation?.id !== operation.id || !states.has(result.operation.state)) throw new Error("Operation status unavailable");
      return result.operation;
    },
    retry: false,
    refetchInterval: (current) => terminal.has(current.state.data?.state ?? "") ? false : 1000,
  });
  const current = query.data;
  const failed = current.state === "Failed" || current.state === "Superseded";
  return (
    <div className="form-panel" role="status" data-testid="operation-status">
      <p><StatusBadge tone={failed ? "danger" : current.state === "Succeeded" ? "ready" : "neutral"}>{current.state}</StatusBadge></p>
      <CopyField value={current.id} label="Copy operation ID" />
      <p>{current.state === "Succeeded" ? "Completed. Resource health is tracked separately." : failed ? "This operation needs attention. The accepted resource is retained." : "Accepted. Waiting for execution."}</p>
      {current.error_class ? <p>Reason: {current.error_class}</p> : null}
      {query.error ? <p>Status temporarily unavailable. The operation remains accepted.</p> : null}
    </div>
  );
}
