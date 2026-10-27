package apiauth

import (
	"context"
	"net/http"

	"mcp-runtime/pkg/passport"
)

const (
	RoleAdmin        = passport.RoleAdmin
	RoleUser         = passport.RoleUser
	UnknownRequestIP = passport.UnknownRequestIP
)

type Principal = passport.Principal
type PrincipalTeam = passport.PrincipalTeam

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return passport.WithPrincipal(ctx, p)
}

func FromContext(ctx context.Context) (Principal, bool) {
	return passport.FromContext(ctx)
}

func RequestIP(r *http.Request) string {
	return passport.RequestIP(r)
}

func RequestSource(r *http.Request) string {
	return passport.RequestSource(r)
}

func AuditSource(r *http.Request, p Principal) string {
	return passport.AuditSource(r, p)
}

func AuditIdentityLabel(p Principal) string {
	return passport.AuditIdentityLabel(p)
}
