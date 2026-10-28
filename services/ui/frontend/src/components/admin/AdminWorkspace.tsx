import { AdminGuard } from "./AdminGuard";
import { type AdminSectionId } from "./adminSections";
import { OperationsPanel } from "./OperationsPanel";
import { TeamsPanel } from "./TeamsPanel";
import { RequestsPanel } from "./RequestsPanel";
import { PlatformOverview } from "./PlatformOverview";
import type { AuthStatus } from "../../api/types";

export type { AdminSectionId };

type AdminWorkspaceProps = {
  auth: AuthStatus;
  onSignIn: () => void;
  // Navigation lives in the application sidebar; this renders only content.
  section?: AdminSectionId;
};

export function AdminWorkspace({ auth, onSignIn, section }: AdminWorkspaceProps) {
  const active = section ?? "overview";
  return (
    <AdminGuard auth={auth} onSignIn={onSignIn}>
      {active === "teams" ? <TeamsPanel onSignIn={onSignIn} /> :
        active === "operations" ? <OperationsPanel onSignIn={onSignIn} /> :
        active === "requests" ? <RequestsPanel onSignIn={onSignIn} /> :
        <PlatformOverview onSignIn={onSignIn} />}
    </AdminGuard>
  );
}
