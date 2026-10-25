import { useMemo, useState } from "react";

import { GrantForm, SessionForm, type GrantDraft, type SessionDraft } from "./accessForms";
import { AsyncSection } from "./AsyncSection";
import { StatusBadge } from "../../ui/Badge";
import { Button } from "../../ui/Button";
import { ConfirmDialog, type ConfirmRequest } from "../../ui/ConfirmDialog";
import { DataTable, buildColumns } from "../../ui/DataTable";
import { SelectField, TextField } from "../../ui/Field";
import { FilterBar, FilterSummary, type FilterChip } from "../../ui/FilterBar";
import { Icon } from "../../ui/Icon";
import { EmptyState } from "../../ui/States";
import { PageHeader } from "../../ui/PageHeader";
import { expiryState, formatAbsolute, formatTimestamp } from "../../lib/format";
import { useAdminReload, useGrants, useSessions } from "../../hooks/useAdminData";
import { useCatalog } from "../../hooks/useCatalog";
import { createGrant, createSession, revokeGrantSessions, setGrantDisabled, setGrantMode, setSessionRevoked } from "../../api/admin";
import { readRuntimeConfig } from "../../api/config";
import { accessKey, subjectLabel } from "../../api/types";
import type { GrantSummary, SessionSummary } from "../../api/types";

export type AccessSelection =
  | { kind: "grant"; item: GrantSummary }
  | { kind: "session"; item: SessionSummary };

type AccessControlPanelProps = {
  namespace: string;
  onNamespaceChange: (namespace: string) => void;
  onSelect: (selection: AccessSelection) => void;
  onSignIn: () => void;
  canManage: boolean;
};

function defaultNamespace(current: string): string {
  return current.trim() || readRuntimeConfig().defaults.namespace || "mcp-servers";
}

function emptyGrantDraft(namespace: string): GrantDraft {
  return {
    mode: "enforce",
    name: "",
    namespace: defaultNamespace(namespace),
    server: "",
    humanID: "",
    agentID: "",
    teamID: "",
    maxTrust: "low",
    allowedSideEffects: ["read"],
    expiresAt: "",
  };
}

function emptySessionDraft(namespace: string): SessionDraft {
  return {
    name: "",
    namespace: defaultNamespace(namespace),
    server: "",
    humanID: "",
    agentID: "",
    teamID: "",
    consentedTrust: "low",
    expiresAt: "",
  };
}

// revoked=false alone does not mean a session can still be used: an expiry in
// the past ends it, and an expiry we cannot parse is reported as unknown.
export function sessionState(session: SessionSummary, now = Date.now()) {
  if (session.revoked) {
    return { tone: "attention" as const, label: "Revoked" };
  }
  switch (expiryState(session.expiresAt, now)) {
    case "expired":
      return { tone: "attention" as const, label: "Expired" };
    case "unparseable":
      return { tone: "unknown" as const, label: "Unknown expiry" };
    default:
      return { tone: "ready" as const, label: "Active" };
  }
}

function grantStatus(grant: GrantSummary, now = Date.now()) {
  if (grant.disabled) return { tone: "attention" as const, label: "Disabled" };
  switch (expiryState(grant.expiresAt, now)) {
    case "expired":
      return { tone: "attention" as const, label: "Expired" };
    case "unparseable":
      return { tone: "unknown" as const, label: "Unknown expiry" };
    default:
      return { tone: "ready" as const, label: "Active" };
  }
}

function expiryText(session: SessionSummary): string {
  switch (expiryState(session.expiresAt)) {
    case "none":
      return "No expiry";
    case "unparseable":
      return `Unreadable (${session.expiresAt})`;
    default:
      return formatTimestamp(session.expiresAt);
  }
}

export function AccessControlPanel({
  namespace,
  onNamespaceChange,
  onSelect,
  onSignIn,
  canManage,
}: AccessControlPanelProps) {
  const [filter, setFilter] = useState("");
  const [busyKey, setBusyKey] = useState("");
  const [actionError, setActionError] = useState("");
  const [actionNotice, setActionNotice] = useState("");
  const [confirm, setConfirm] = useState<ConfirmRequest | null>(null);
  const [grantDraft, setGrantDraft] = useState<GrantDraft | null>(null);
  const [sessionDraft, setSessionDraft] = useState<SessionDraft | null>(null);
  const reload = useAdminReload();

  const grantsQuery = useGrants(true, namespace);
  const sessionsQuery = useSessions(true, namespace);
  // The same authorized catalog the Servers screen reads, so the create forms
  // can offer real server and namespace choices instead of free text.
  const catalog = useCatalog(true, namespace);

  const grants = grantsQuery.data ?? [];
  const sessions = sessionsQuery.data ?? [];

  const term = filter.trim().toLowerCase();
  const matches = (parts: Array<string | undefined>) =>
    !term || parts.filter(Boolean).join(" ").toLowerCase().includes(term);

  const visibleGrants = grants.filter((grant) =>
    matches([grant.name, grant.namespace, grant.serverRef?.name, subjectLabel(grant.subject)])
  );
  const visibleSessions = sessions.filter((session) =>
    matches([session.name, session.namespace, session.serverRef?.name, subjectLabel(session.subject)])
  );

  const grantsLoaded = grantsQuery.isSuccess;
  const sessionsLoaded = sessionsQuery.isSuccess;
  const activeGrants = grants.filter((grant) => grantStatus(grant).label === "Active").length;
  const activeSessions = sessions.filter((session) => sessionState(session).label === "Active").length;
  const namespaceOptions = useMemo(() => {
    const names = new Set(catalog.namespaces.map((entry) => entry.namespace));
    names.add(defaultNamespace(namespace));
    return [...names].filter(Boolean).sort();
  }, [catalog.namespaces, namespace]);

  async function runAction(key: string, label: string, action: () => Promise<void>) {
    setBusyKey(key);
    setActionError("");
    setActionNotice("");
    try {
      await action();
      setActionNotice(label);
      reload();
    } catch (error) {
      setActionError(error instanceof Error ? error.message : "The change could not be applied.");
    } finally {
      setBusyKey("");
      setConfirm(null);
    }
  }

  function askToggleGrant(grant: GrantSummary) {
    const disable = !grant.disabled;
    setConfirm({
      title: `${disable ? "Disable" : "Enable"} grant "${grant.name}"?`,
      body: disable
        ? `The gateway will stop matching this grant, so ${subjectLabel(grant.subject)} loses access to ${grant.serverRef?.name || "its server"} unless another grant allows it.`
        : `The gateway will match this grant again, restoring ${subjectLabel(grant.subject)}'s access up to ${grant.maxTrust || "its"} trust.`,
      confirmLabel: disable ? "Disable grant" : "Enable grant",
      destructive: disable,
      onConfirm: () =>
        runAction(
          `grant:${accessKey(grant)}`,
          `Grant "${grant.name}" ${disable ? "disabled" : "enabled"}.`,
          () => setGrantDisabled(grant.namespace, grant.name, disable)
        ),
    });
  }

  function askChangeGrantMode(grant: GrantSummary) {
    const mode = grant.mode === "monitor" ? "enforce" : "monitor";
    setConfirm({
      title: `Set grant "${grant.name}" to ${mode}?`,
      body: mode === "monitor"
        ? "This grant will record policy denials without blocking calls. Identity, session and other enforcing grant checks still apply."
        : "This grant will block calls that exceed its tool, trust or side-effect restrictions.",
      confirmLabel: `Set to ${mode}`,
      destructive: false,
      onConfirm: () => runAction(`mode:${accessKey(grant)}`, `Grant "${grant.name}" set to ${mode}.`, () => setGrantMode(grant.namespace, grant.name, mode)),
    });
  }

  function askToggleSession(session: SessionSummary) {
    const revoke = !session.revoked;
    setConfirm({
      title: `${revoke ? "Revoke" : "Restore"} session "${session.name}"?`,
      body: revoke
        ? `In-flight and future tool calls made with this session are denied immediately.`
        : `The session becomes usable again until its expiry (${expiryText(session)}).`,
      confirmLabel: revoke ? "Revoke session" : "Restore session",
      destructive: revoke,
      onConfirm: () =>
        runAction(
          `session:${accessKey(session)}`,
          `Session "${session.name}" ${revoke ? "revoked" : "restored"}.`,
          () => setSessionRevoked(session.namespace, session.name, revoke)
        ),
    });
  }

  function askRevokeGrantSessions(grant: GrantSummary) {
    setConfirm({
      title: `Revoke sessions created from grant "${grant.name}"?`,
      body: "Every active session linked to this grant will be revoked. The grant itself remains enabled.",
      confirmLabel: "Revoke sessions",
      destructive: true,
      onConfirm: () => runAction(
        `grant-sessions:${accessKey(grant)}`,
        `Sessions from grant "${grant.name}" revoked.`,
        async () => { await revokeGrantSessions(grant.namespace, grant.name); }
      ),
    });
  }

  function subjectCell(subject: GrantSummary["subject"]) {
    const entries = [["Human", subject?.humanID], ["Agent", subject?.agentID], ["Team", subject?.teamID]].filter(([, value]) => value);
    return entries.length ? <dl className="access-subject">{entries.map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl> : <span className="muted">Unreported subject</span>;
  }
  const grantColumns = useMemo(() => buildColumns<GrantSummary>([
    { id: "name", header: "Grant", rowHeader: true, sortValue: grant => grant.name, cell: grant => <><button type="button" className="link-button" data-testid="grant-drilldown" onClick={() => onSelect({ kind: "grant", item: grant })}>{grant.name}</button><span className="cell-detail">{grant.namespace}</span></> },
    { id: "server", header: "Server", sortValue: grant => grant.serverRef?.name || "", cell: grant => grant.serverRef?.name || "—" },
    { id: "subject", header: "Subject", cell: grant => subjectCell(grant.subject) },
    { id: "limits", header: "Access limits", cell: grant => <><span>{grant.maxTrust || "Unreported"} trust ceiling</span><span className="cell-detail">{(grant.allowedSideEffects || []).join(", ") || "No side effects reported"}</span></> },
    { id: "expires", header: "Expires", sortValue: grant => grant.expiresAt || "", cell: grant => grant.expiresAt ? formatTimestamp(grant.expiresAt) : "No expiry" },
    { id: "status", header: "Status", sortValue: grant => grantStatus(grant).label, cell: grant => <><StatusBadge tone={grantStatus(grant).tone}>{grantStatus(grant).label}</StatusBadge><span className="cell-detail">Mode: {grant.mode || "enforce"}</span></> },
    { id: "actions", header: "Actions", cell: grant => canManage ? <details className="access-row-actions"><summary aria-label={`Actions for grant ${grant.name}`}>Actions <Icon name="chevronDown" size={14} /></summary><div>
      <Button variant="ghost" size="sm" data-testid="grant-toggle" busy={busyKey === `grant:${accessKey(grant)}`} onClick={() => askToggleGrant(grant)}>{grant.disabled ? "Enable" : "Disable"}</Button>
      <Button variant="ghost" size="sm" data-testid="grant-mode-toggle" busy={busyKey === `mode:${accessKey(grant)}`} onClick={() => askChangeGrantMode(grant)}>{grant.mode === "monitor" ? "Enforce" : "Monitor"}</Button>
      <Button variant="danger-outline" size="sm" data-testid="grant-revoke-sessions" busy={busyKey === `grant-sessions:${accessKey(grant)}`} onClick={() => askRevokeGrantSessions(grant)}>Revoke sessions</Button>
    </div></details> : null },
  ]).filter(column => canManage || column.id !== "actions"), [busyKey, onSelect, canManage]);
  const sessionColumns = useMemo(() => buildColumns<SessionSummary>([
    { id: "name", header: "Session", rowHeader: true, sortValue: session => session.name, cell: session => <><button type="button" className="link-button" data-testid="session-drilldown" onClick={() => onSelect({ kind: "session", item: session })}>{session.name}</button><span className="cell-detail">{session.namespace}</span></> },
    { id: "server", header: "Server", sortValue: session => session.serverRef?.name || "", cell: session => session.serverRef?.name || "—" },
    { id: "subject", header: "Subject", cell: session => subjectCell(session.subject) },
    { id: "trust", header: "Consented trust", sortValue: session => session.consentedTrust || "", cell: session => session.consentedTrust || "—" },
    { id: "expires", header: "Expires", sortValue: session => Date.parse(session.expiresAt || "") || 0, cell: session => <span title={formatAbsolute(session.expiresAt)}>{expiryText(session)}</span> },
    { id: "status", header: "Status", cell: session => { const state = sessionState(session); return <StatusBadge tone={state.tone}>{state.label}</StatusBadge>; } },
    { id: "actions", header: "Actions", cell: session => canManage ? <details className="access-row-actions"><summary aria-label={`Actions for session ${session.name}`}>Actions <Icon name="chevronDown" size={14} /></summary><div><Button variant={session.revoked ? "secondary" : "danger-outline"} size="sm" data-testid="session-toggle" busy={busyKey === `session:${accessKey(session)}`} onClick={() => askToggleSession(session)}>{session.revoked ? "Restore" : "Revoke"}</Button></div></details> : null },
  ]).filter(column => canManage || column.id !== "actions"), [busyKey, onSelect, canManage]);

  const chips: FilterChip[] = [];
  if (filter.trim()) {
    chips.push({ id: "filter", label: "Search", value: filter.trim(), onRemove: () => setFilter("") });
  }
  if (namespace.trim()) {
    chips.push({
      id: "namespace",
      label: "Namespace",
      value: namespace.trim(),
      onRemove: () => onNamespaceChange(""),
    });
  }

  return (
    <div className="access-page">
      <PageHeader
        title="Access control"
        description="Manage server permissions and inspect the sessions issued under them."
        actions={
          <>
            <Button variant="secondary" icon="refresh" onClick={reload} data-testid="access-refresh">
              Refresh
            </Button>
            {canManage ? <Button
              variant="primary"
              icon="plus"
              data-testid="grant-create-toggle"
              onClick={() => {
                setSessionDraft(null);
                setGrantDraft((current) => (current ? null : emptyGrantDraft(namespace)));
              }}
            >
              New grant
            </Button> : null}
            {canManage ? <Button
              variant="secondary"
              icon="plus"
              data-testid="session-create-toggle"
              onClick={() => {
                setGrantDraft(null);
                setSessionDraft((current) => (current ? null : emptySessionDraft(namespace)));
              }}
            >
              New session
            </Button> : null}
          </>
        }
      />

      <p className="access-introduction"><Icon name="shield" /> Grants define permission. Sessions use that permission for a bounded period.</p>
      <div role="group" aria-label="Access control summary" data-testid="access-stats"><dl className="access-summary">{[
        { label: "Active grants", value: grantsLoaded ? activeGrants : undefined },
        { label: "Inactive grants", value: grantsLoaded ? grants.length - activeGrants : undefined },
        { label: "Active sessions", value: sessionsLoaded ? activeSessions : undefined },
        { label: "Other sessions", value: sessionsLoaded ? sessions.length - activeSessions : undefined },
      ].map(metric => <div key={metric.label}><dt>{metric.label}</dt><dd>{metric.value === undefined ? "—" : metric.value.toLocaleString()}</dd></div>)}</dl></div>
      <section className="access-filter-panel" aria-label="Access record filters">
      <FilterBar label="Filter access records">
        <TextField
          label="Search"
          type="search"
          fieldClassName="grow"
          leadingIcon
          placeholder="Name, server, human, agent, or team…"
          name="access-search"
          autoComplete="off"
          value={filter}
          data-testid="access-filter"
          onChange={(event) => setFilter(event.target.value)}
        />
        <SelectField
          label="Namespace"
          value={namespace}
          data-testid="access-namespace"
          hint="Records are stored and managed within a namespace."
          options={[
            { value: "", label: "All namespaces" },
            ...namespaceOptions.map((ns) => ({ value: ns, label: ns })),
          ]}
          onChange={(event) => onNamespaceChange(event.target.value)}
        />
      </FilterBar>

      <FilterSummary
        chips={chips}
        count={`${grantsLoaded ? `${visibleGrants.length} ${visibleGrants.length === 1 ? "grant" : "grants"}` : grantsQuery.isPending ? "Loading grants…" : "Grant count unavailable"}, ${sessionsLoaded ? `${visibleSessions.length} ${visibleSessions.length === 1 ? "session" : "sessions"}` : sessionsQuery.isPending ? "loading sessions…" : "session count unavailable"}`}
        onClear={() => {
          setFilter("");
          onNamespaceChange("");
        }}
        testId="access-summary"
      />
      </section>
      {!canManage ? <p className="access-readonly"><Icon name="info" /> View-only access. A team owner or platform administrator can change permissions.</p> : null}

      {actionNotice ? (
        <p className="notice notice-success" role="status" data-testid="access-action-notice">
          <Icon name="check" />
          <span className="notice-body">{actionNotice}</span>
        </p>
      ) : null}
      {actionError ? (
        <p className="notice notice-danger" role="alert" data-testid="access-action-error">
          <Icon name="alert" />
          <span className="notice-body">{actionError}</span>
        </p>
      ) : null}

      {grantDraft ? (
        <GrantForm
          draft={grantDraft}
          servers={catalog.servers}
          namespaces={namespaceOptions}
          busy={busyKey === "create-grant"}
          submitError={actionError}
          onChange={setGrantDraft}
          onCancel={() => setGrantDraft(null)}
          onSubmit={() =>
            void runAction("create-grant", `Grant "${grantDraft.name}" created.`, async () => {
              await createGrant({
                mode: grantDraft.mode || "enforce",
                name: grantDraft.name.trim(),
                namespace: grantDraft.namespace.trim(),
                serverRef: { name: grantDraft.server.trim() },
                subject: {
                  humanID: grantDraft.humanID.trim() || undefined,
                  agentID: grantDraft.agentID.trim() || undefined,
                  teamID: grantDraft.teamID.trim() || undefined,
                },
                maxTrust: grantDraft.maxTrust,
                allowedSideEffects: grantDraft.allowedSideEffects,
                expiresAt: grantDraft.expiresAt ? new Date(grantDraft.expiresAt).toISOString() : undefined,
              });
              setGrantDraft(null);
            })
          }
        />
      ) : null}

      {sessionDraft ? (
        <SessionForm
          draft={sessionDraft}
          servers={catalog.servers}
          namespaces={namespaceOptions}
          busy={busyKey === "create-session"}
          submitError={actionError}
          onChange={setSessionDraft}
          onCancel={() => setSessionDraft(null)}
          onSubmit={() =>
            void runAction("create-session", `Session "${sessionDraft.name}" created.`, async () => {
              await createSession({
                name: sessionDraft.name.trim(),
                namespace: sessionDraft.namespace.trim(),
                serverRef: { name: sessionDraft.server.trim() },
                subject: {
                  humanID: sessionDraft.humanID.trim() || undefined,
                  agentID: sessionDraft.agentID.trim() || undefined,
                  teamID: sessionDraft.teamID.trim() || undefined,
                },
                consentedTrust: sessionDraft.consentedTrust,
                expiresAt: sessionDraft.expiresAt
                  ? new Date(sessionDraft.expiresAt).toISOString()
                  : undefined,
              });
              setSessionDraft(null);
            })
          }
        />
      ) : null}

      <section className="access-directory" aria-labelledby="grants-title" data-testid="grants-panel">
        <div className="access-directory-head">
          <h2 id="grants-title">
            Access grants
          </h2>
          <p className="section-note">What a subject is allowed to do on a server.</p>
        </div>
        <AsyncSection
          query={grantsQuery}
          loadingLabel="Loading access grants…"
          errorTitle="Access grants could not be loaded."
          onRetry={reload}
          onSignIn={onSignIn}
          testId="grants"
        >
          {grants.length ? <DataTable
            columns={grantColumns}
            rows={visibleGrants}
            rowKey={accessKey}
            caption="Access grants with server, subject, trust ceiling, allowed side effects, and status."
            regionLabel="Grant directory table"
            testId="grants-table"
            minWidth={canManage ? 1050 : 900}
            emptyMessage={
              grants.length === 0 ? "No access grants found." : "No grants match this filter."
            }
          /> : <EmptyState icon="shield" title="No access grants yet" detail={canManage ? "Create a grant to define which human, agent or team can use a server, with explicit trust and side-effect limits." : "A team owner can grant access to a server. Granted permissions will appear here."} action={canManage ? <Button variant="primary" icon="plus" onClick={() => { setSessionDraft(null); setGrantDraft(current => current || emptyGrantDraft(namespace)); }}>Create a grant</Button> : undefined} testId="grants-empty" />}
        </AsyncSection>
      </section>

      <section className="access-directory" aria-labelledby="sessions-title" data-testid="sessions-panel">
        <div className="access-directory-head">
          <h2 id="sessions-title">
            Agent sessions
          </h2>
          <p className="section-note">What an agent consented to for a bounded period.</p>
        </div>
        <AsyncSection
          query={sessionsQuery}
          loadingLabel="Loading agent sessions…"
          errorTitle="Agent sessions could not be loaded."
          onRetry={reload}
          onSignIn={onSignIn}
          testId="sessions"
        >
          {sessions.length ? <DataTable
            columns={sessionColumns}
            rows={visibleSessions}
            rowKey={accessKey}
            caption="Agent sessions with server, subject, consented trust, expiry, and status."
            regionLabel="Session directory table"
            testId="sessions-table"
            minWidth={canManage ? 1000 : 850}
            emptyMessage={
              sessions.length === 0 ? "No agent sessions found." : "No sessions match this filter."
            }
          /> : <EmptyState icon="key" title="No agent sessions yet" detail="A session records the identity, server, consented trust and expiry used for a connection. Create an access grant first, then connect an agent or issue a session." action={canManage ? <Button variant="secondary" icon="plus" onClick={() => { setGrantDraft(null); setSessionDraft(current => current || emptySessionDraft(namespace)); }}>Create a session</Button> : undefined} testId="sessions-empty" />}
        </AsyncSection>
      </section>
      <details className="access-explanation"><summary>How grants and sessions work together</summary><p>Grants set a server’s permitted subjects, trust ceiling and side effects. Sessions record consent for a particular identity and period. Active counts exclude expired or unreadable expiry values; inactive grants include disabled and expired records. Other sessions include revoked, expired and unknown-expiry records. Open a record to inspect its gateway decisions.</p></details>

      {confirm ? (
        <ConfirmDialog
          {...confirm}
          busy={busyKey !== ""}
          onCancel={() => setConfirm(null)}
          testId="access-confirm"
          confirmTestId="access-confirm-yes"
          cancelTestId="access-confirm-cancel"
        />
      ) : null}
    </div>
  );
}
