package platformauth

// Wire contracts of the platform-api principal endpoints that the resolvers in
// this package call.

// AuthResolveRequest is POST /internal/auth/resolve.
type AuthResolveRequest struct {
	APIKey string `json:"api_key"`
}

// AuthResolveResponse is POST /internal/auth/resolve.
type AuthResolveResponse struct {
	OK        bool      `json:"ok"`
	Principal Principal `json:"principal,omitempty"`
}

// PrincipalResolveRequest is POST /internal/identity/principal.
type PrincipalResolveRequest struct {
	UserID string `json:"user_id"`
}

// PrincipalResolveResponse is POST /internal/identity/principal.
type PrincipalResolveResponse struct {
	Principal Principal `json:"principal"`
}
