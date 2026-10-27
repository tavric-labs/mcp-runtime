// Package apiwire holds the request and response types for platform-api
// endpoints that more than one module calls: the /internal/* service-to-service
// endpoints and shared records such as TeamMembership and UserDeactivationResult.
//
// Provider and consumer import the same structs, so renaming a field fails to
// compile on both sides of the split. The package carries wire shapes only: no
// handlers, storage or business rules.
package apiwire
