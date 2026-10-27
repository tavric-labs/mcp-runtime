package runtimeapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"mcp-runtime-api/internal/platformapi"
	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/envcfg"
	"mcp-runtime/pkg/httpx"
	"mcp-runtime/pkg/passport"
	"mcp-runtime/pkg/topology"
)

const (
	roleAdmin               = passport.RoleAdmin
	roleUser                = passport.RoleUser
	sharedCatalogNamespace  = topology.MCPServersNamespace
	teamNamespacePrefix     = "mcp-team-"
	teamRoleOwner           = "owner"
	teamRoleMember          = "member"
	namespaceScopeUser      = "user"
	namespaceScopeTeam      = "team"
	accessApplyMaxBytes     = 64 * 1024
	deploymentApplyMaxBytes = 32 * 1024
	teamApplyMaxBytes       = 8 * 1024
)

// Principal is the authenticated platform identity contract shared by runtime handlers.
type Principal = passport.Principal
type principal = passport.Principal
type principalTeam = passport.PrincipalTeam
type teamRecord = platformapi.Team
type teamMembershipRecord = platformapi.TeamMembership
type auditEvent = platformapi.AuditEvent

type identityStore interface {
	ListTeams(ctx context.Context) ([]teamRecord, error)
	GetTeamBySlug(ctx context.Context, slug string) (teamRecord, bool, error)
	CreateAgent(ctx context.Context, teamSlug, name, createdBy string) (platformapi.Agent, error)
	ListAgents(ctx context.Context, teamSlug, status, q, cursor string, limit int) (platformapi.AgentPage, error)
	GetAgent(ctx context.Context, id string) (platformapi.Agent, bool, error)
	RenameAgent(ctx context.Context, id, name string) (platformapi.Agent, error)
	SetAgentStatus(ctx context.Context, id, status, actorID string) (platformapi.Agent, error)
	CreateTeam(ctx context.Context, slug, name, createdByUserID string) (teamRecord, error)
	DeleteTeamBySlug(ctx context.Context, slug string) error
	ListNamespaces(ctx context.Context) ([]map[string]any, error)
	GetNamespace(ctx context.Context, namespace string) (map[string]any, bool, error)
	ListTeamMemberships(ctx context.Context, teamSlug string) ([]teamMembershipRecord, error)
	UpsertTeamMembership(ctx context.Context, teamSlug, userID, role string) (teamMembershipRecord, error)
	DeleteTeamMembership(ctx context.Context, teamSlug, userID string) error
	CreatePasswordUser(ctx context.Context, email, password, role string) (platformapi.User, error)
	CreateTeamUser(ctx context.Context, teamSlug, email, password, role string) (platformapi.User, teamMembershipRecord, error)
	Configured() bool
}

type auditWriter interface {
	WriteAudit(context.Context, auditEvent)
}

func principalFromContext(ctx context.Context) (principal, bool) {
	return passport.FromContext(ctx)
}

func withPrincipal(ctx context.Context, p principal) context.Context {
	return passport.WithPrincipal(ctx, p)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	httpx.WriteJSON(w, status, payload)
}

func writeBodyDecodeError(w http.ResponseWriter, err error) {
	apikit.WriteBodyDecodeError(w, err)
}

func writeAPIError(w http.ResponseWriter, status int, message string, cause ...error) {
	writeAPIErrorCode(w, status, stableCodeForStatus(status), message, cause...)
}

func writeAPIErrorCode(w http.ResponseWriter, status int, code, message string, cause ...error) {
	apikit.WriteError(w, zap.L(), newAPIError(status, code, message, cause...))
}

func stableCodeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return apikit.CodeInvalidRequestBody
	case http.StatusUnauthorized:
		return apikit.CodeUnauthorized
	case http.StatusForbidden:
		return apikit.CodeForbidden
	case http.StatusNotFound:
		return apikit.CodeNotFound
	case http.StatusConflict:
		return apikit.CodeConflict
	case http.StatusMethodNotAllowed:
		return apikit.CodeMethodNotAllowed
	case http.StatusRequestEntityTooLarge:
		return apikit.CodeRequestTooLarge
	case http.StatusServiceUnavailable:
		return apikit.CodeKubernetesUnavailable
	default:
		return apikit.CodeInternalError
	}
}

func newAPIError(status int, code, message string, cause ...error) *apikit.Error {
	switch status {
	case http.StatusBadRequest:
		return apikit.BadRequest(code, message, cause...)
	case http.StatusUnauthorized:
		return apikit.Unauthorized(message, cause...)
	case http.StatusForbidden:
		return apikit.Forbidden(message, cause...)
	case http.StatusNotFound:
		return apikit.NotFound(message, cause...)
	case http.StatusConflict:
		return apikit.Conflict(message, cause...)
	case http.StatusInternalServerError:
		return apikit.Internal(message, cause...)
	case http.StatusServiceUnavailable:
		return apikit.ServiceUnavailable(code, message, cause...)
	default:
		return &apikit.Error{Status: status, Code: code, Message: message, Cause: errors.Join(cause...)}
	}
}

func requestIP(r *http.Request) string {
	return passport.RequestIP(r)
}

func auditSource(r *http.Request, p principal) string {
	return passport.AuditSource(r, p)
}

func auditIdentityLabel(p principal) string {
	return passport.AuditIdentityLabel(p)
}

func envOr(key, fallback string) string {
	return envcfg.EnvOr(key, fallback)
}

// NormalizeTeamSlug canonicalizes a team slug using the platform store rules shared with identity APIs.
func NormalizeTeamSlug(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	raw = strings.ReplaceAll(raw, "_", "-")
	return raw
}
