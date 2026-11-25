package policy

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

// Identity is the subject context used to evaluate a rendered policy document.
type Identity struct {
	HumanID   HumanID
	AgentID   AgentID
	TeamID    TeamID
	SessionID SessionID
}

// Request describes the MCP RPC request being evaluated.
type Request struct {
	Identity  Identity
	RPCMethod string
	ToolName  ToolName
	// Target is the resource URI, prompt name or skill URI of a primitive request.
	Target string
	// Targets are the resource URIs a subscriptions/listen request opts into.
	Targets []string
	// Session is the binding resolved from the synchronized platform store.
	// It is request-scoped and never changes the immutable policy document.
	Session *Binding
}

// Decision is the result of evaluating a rendered policy document.
type Decision struct {
	Allowed bool
	Status  int
	Reason  string
	// WouldDeny is true when a matched monitor-mode grant would have denied this
	// request. The request is allowed only after the normal identity and session
	// checks have succeeded.
	WouldDeny          bool
	WouldDenyReason    string
	PolicyVersion      string
	RequiredTrust      string
	RequiredSideEffect string
	RiskLevel          string
	AdminTrust         string
	ConsentedTrust     string
	EffectiveTrust     string
	// MatchedGrant is the name of the grant that determined this decision,
	// empty when no grant applied (e.g. no_matching_grant, default decisions).
	// MatchedGrantNamespace qualifies it: cross-namespace grants targeting the
	// same server may share a name.
	MatchedGrant          string
	MatchedGrantNamespace string
	// MatchedSession is the name of the session binding the request resolved
	// to, empty when no live session applied to the decision.
	// MatchedSessionNamespace qualifies it for the same reason.
	MatchedSession          string
	MatchedSessionNamespace string
	// ConsideredGrantCount and ConsideredGrantNames are set only for a
	// no_matching_grant denial. They report how many grants targeted this
	// server and the first few names, so an operator can compare them with the
	// presented identity. They are for logs and audit only and are never
	// written to the client response.
	ConsideredGrantCount int
	ConsideredGrantNames string
}

// maxConsideredGrantNames bounds how many grant names a denial reports.
const maxConsideredGrantNames = 10

// Deny builds a denied authorization decision.
func Deny(status int, reason, policyVersion string) Decision {
	return Decision{
		Status:        status,
		Reason:        reason,
		PolicyVersion: policyVersion,
	}
}

// Allow builds an allowed decision with an explicit reason. It is used for
// requests that are intentionally not subject to grant/session evaluation
// (non-tool-call passthrough, OAuth metadata) so that the gateway's default
// decision can be deny — making any path that reaches upstream without an
// explicit decision fail closed.
func Allow(reason, policyVersion string) Decision {
	return Decision{
		Allowed:       true,
		Status:        http.StatusOK,
		Reason:        reason,
		PolicyVersion: policyVersion,
	}
}

// Authorize evaluates a rendered gateway policy document for a single MCP RPC request.
func Authorize(policy *Document, request Request, now time.Time) Decision {
	if policy == nil || UsesDelegatedHeaders(policy) || policyModeObserve(policy) || !IsToolCallMethod(request.RPCMethod) {
		return authorize(policy, request, now)
	}
	if policy.compiled != nil && !policy.compiled.hasMonitor {
		return authorize(policy, request, now)
	}
	if now.IsZero() {
		now = time.Now()
	}
	var enforcing, monitoring []Grant
	var activeEnforcing, activeMonitoring bool
	for _, grant := range matchingPolicyGrants(policy, request.Identity) {
		active := subjectMatchesTeam(grant.HumanID, grant.AgentID, grant.TeamID, request.Identity) && !grant.Disabled && !isExpiredAt(grant.ExpiresAt, now)
		if strings.EqualFold(strings.TrimSpace(grant.Mode), "monitor") {
			monitoring = append(monitoring, grant)
			activeMonitoring = activeMonitoring || active
		} else {
			enforcing = append(enforcing, grant)
			activeEnforcing = activeEnforcing || active
		}
	}
	if !activeMonitoring {
		return authorize(policy, request, now)
	}
	if _, sideEffect, _ := resolvePolicyToolMetadata(policy, request.ToolName); sideEffect == "" {
		// Missing server metadata is not a restriction attributable to a grant.
		return authorize(policy, request, now)
	}
	// Monitor grants cannot contribute permissions or restrictions to the
	// enforcing policy. Evaluate both groups independently with the same identity
	// and session gates, preserving existing aggregation within each group.
	shadow := authorize(policyWithGrants(policy, monitoring), request, now)
	decision := shadow
	if activeEnforcing {
		decision = authorize(policyWithGrants(policy, enforcing), request, now)
		if !decision.Allowed {
			return decision
		}
	}
	if monitorableGrantDenial(shadow.Reason) {
		decision.Allowed = true
		decision.Status = http.StatusOK
		decision.WouldDeny = true
		decision.WouldDenyReason = shadow.Reason
	}
	return decision
}

func authorize(policy *Document, request Request, now time.Time) Decision {
	if UsesDelegatedHeaders(policy) {
		return authorizeDelegated(policy, request)
	}
	decision := Decision{
		Allowed:       true,
		Status:        http.StatusOK,
		Reason:        "allowed",
		PolicyVersion: policyVersionOrDefault(policy, ""),
	}
	if !IsToolCallMethod(request.RPCMethod) {
		if IsPrimitiveMethod(request.RPCMethod) && !policyModeObserve(policy) {
			return authorizePrimitive(policy, request, now)
		}
		return decision
	}
	_, _, decision.RiskLevel = resolvePolicyToolMetadata(policy, request.ToolName)
	if policyModeObserve(policy) {
		return decision
	}

	identity := request.Identity
	if now.IsZero() {
		now = time.Now()
	}
	caller, denied := resolveCaller(policy, request, now)
	if denied != nil {
		return *denied
	}
	session, sessionFound := caller.session, caller.found
	matchedSession, matchedSessionNamespace := caller.name, caller.namespace

	requiredTrust, requiredSideEffect, riskLevel := resolvePolicyToolMetadata(policy, request.ToolName)
	matchingGrants := matchingPolicyGrants(policy, identity)
	if len(matchingGrants) == 0 {
		denied := decideByDefault(policy, "no_matching_grant")
		denied.ConsideredGrantCount, denied.ConsideredGrantNames = consideredGrants(policy)
		denied.MatchedSession = matchedSession
		denied.MatchedSessionNamespace = matchedSessionNamespace
		return denied
	}

	grant := bestGrantFor(matchingGrants, request.ToolName, requiredTrust, requiredSideEffect, policyVersionOrDefault(policy, ""), now)
	if grant.deny != nil {
		denied := *grant.deny
		denied.MatchedSession = matchedSession
		denied.MatchedSessionNamespace = matchedSessionNamespace
		return denied
	}
	if !grant.toolAllowed {
		reason := "tool_not_granted"
		if grant.onlyExpired {
			// Every matching grant that is not disabled has expired.
			reason = "grant_expired"
		}
		denied := decideByDefault(policy, reason)
		denied.MatchedSession = matchedSession
		denied.MatchedSessionNamespace = matchedSessionNamespace
		return denied
	}
	if requiredSideEffect == "" {
		return Decision{
			Status:                  http.StatusForbidden,
			Reason:                  "tool_side_effect_unknown",
			PolicyVersion:           grant.policyVersion,
			RequiredTrust:           grant.requiredTrust,
			RiskLevel:               riskLevel,
			MatchedGrant:            grant.grantName,
			MatchedGrantNamespace:   grant.grantNamespace,
			MatchedSession:          matchedSession,
			MatchedSessionNamespace: matchedSessionNamespace,
		}
	}
	if !grant.sideEffectAllowed {
		return Decision{
			Status:                  http.StatusForbidden,
			Reason:                  "side_effect_not_allowed",
			PolicyVersion:           grant.policyVersion,
			RequiredTrust:           grant.requiredTrust,
			RequiredSideEffect:      requiredSideEffect,
			RiskLevel:               riskLevel,
			MatchedGrant:            grant.grantName,
			MatchedGrantNamespace:   grant.grantNamespace,
			MatchedSession:          matchedSession,
			MatchedSessionNamespace: matchedSessionNamespace,
		}
	}
	if grant.adminTrustRank == 0 {
		denied := decideByDefault(policy, "grant_without_trust")
		denied.MatchedGrant = grant.grantName
		denied.MatchedGrantNamespace = grant.grantNamespace
		denied.MatchedSession = matchedSession
		denied.MatchedSessionNamespace = matchedSessionNamespace
		return denied
	}

	consentedRank := grant.adminTrustRank
	consentedTrust := RankToTrust(grant.adminTrustRank)
	if sessionFound && session.ConsentedTrust != "" {
		consentedRank = TrustRank(session.ConsentedTrust)
		consentedTrust = RankToTrust(consentedRank)
	}
	effectiveRank := minInt(grant.adminTrustRank, consentedRank)
	if effectiveRank < grant.requiredTrustRank {
		return Decision{
			Status:                  http.StatusForbidden,
			Reason:                  "trust_too_low",
			PolicyVersion:           grant.policyVersion,
			RequiredTrust:           grant.requiredTrust,
			RequiredSideEffect:      requiredSideEffect,
			RiskLevel:               riskLevel,
			AdminTrust:              RankToTrust(grant.adminTrustRank),
			ConsentedTrust:          consentedTrust,
			EffectiveTrust:          RankToTrust(effectiveRank),
			MatchedGrant:            grant.grantName,
			MatchedGrantNamespace:   grant.grantNamespace,
			MatchedSession:          matchedSession,
			MatchedSessionNamespace: matchedSessionNamespace,
		}
	}

	return Decision{
		Allowed:                 true,
		Status:                  http.StatusOK,
		Reason:                  "allowed",
		PolicyVersion:           grant.policyVersion,
		RequiredTrust:           grant.requiredTrust,
		RequiredSideEffect:      requiredSideEffect,
		RiskLevel:               riskLevel,
		AdminTrust:              RankToTrust(grant.adminTrustRank),
		ConsentedTrust:          consentedTrust,
		EffectiveTrust:          RankToTrust(effectiveRank),
		MatchedGrant:            grant.grantName,
		MatchedGrantNamespace:   grant.grantNamespace,
		MatchedSession:          matchedSession,
		MatchedSessionNamespace: matchedSessionNamespace,
	}
}

func monitorableGrantDenial(reason string) bool {
	switch reason {
	case "tool_denied", "tool_not_granted", "side_effect_not_allowed", "trust_too_low":
		return true
	default:
		return false
	}
}

type grantSelection struct {
	toolAllowed       bool
	sideEffectAllowed bool
	adminTrustRank    int
	requiredTrustRank int
	requiredTrust     string
	policyVersion     string
	grantName         string
	grantNamespace    string
	deny              *Decision
	// onlyExpired is true when every matching grant that is not disabled has
	// passed its expires_at, so the denial reason can say so.
	onlyExpired bool
}

func bestGrantFor(grants []Grant, toolName ToolName, requiredTrust, requiredSideEffect, policyVersion string, now time.Time) (selection grantSelection) {
	selection = grantSelection{
		requiredTrustRank: TrustRank(requiredTrust),
		requiredTrust:     requiredTrust,
		policyVersion:     policyVersion,
	}
	// attribute records the grant a decision is reported against. The policy
	// version moves with the attributed grant so the two never disagree.
	attribute := func(grant Grant) {
		selection.grantName = grant.Name
		selection.grantNamespace = string(grant.Namespace)
		selection.policyVersion = ChoosePolicyVersion(grant.PolicyVersion, policyVersion)
	}
	var expired, active int
	defer func() { selection.onlyExpired = expired > 0 && active == 0 }()
	sorted := append([]Grant(nil), grants...)
	sort.Slice(sorted, func(i, j int) bool {
		left := string(sorted[i].Namespace) + "\x00" + sorted[i].Name
		right := string(sorted[j].Namespace) + "\x00" + sorted[j].Name
		return left < right
	})
	for _, grant := range sorted {
		if grant.Disabled {
			continue
		}
		if isExpiredAt(grant.ExpiresAt, now) {
			expired++
			continue
		}
		active++
		adminRank := TrustRank(grant.MaxTrust)
		if len(grant.ToolRules) == 0 {
			selection.toolAllowed = true
			if selection.grantName == "" {
				attribute(grant)
			}
			if sideEffectAllowed(grant.AllowedSideEffects, requiredSideEffect) {
				if !selection.sideEffectAllowed || adminRank > selection.adminTrustRank {
					attribute(grant)
				}
				selection.sideEffectAllowed = true
				selection.adminTrustRank = maxInt(selection.adminTrustRank, adminRank)
			}
			continue
		}
		for _, rule := range grant.ToolRules {
			if rule.Name != toolName {
				continue
			}
			if strings.EqualFold(rule.Decision, "deny") {
				deny := Deny(http.StatusForbidden, "tool_denied", ChoosePolicyVersion(grant.PolicyVersion, policyVersion))
				deny.MatchedGrant = grant.Name
				deny.MatchedGrantNamespace = string(grant.Namespace)
				selection.deny = &deny
				return selection
			}
			selection.toolAllowed = true
			if selection.grantName == "" {
				attribute(grant)
			}
			if sideEffectAllowed(grant.AllowedSideEffects, requiredSideEffect) {
				if !selection.sideEffectAllowed || adminRank > selection.adminTrustRank {
					attribute(grant)
				}
				selection.sideEffectAllowed = true
				ruleRank := TrustRank(rule.RequiredTrust)
				if ruleRank > selection.requiredTrustRank {
					selection.requiredTrustRank = ruleRank
					selection.requiredTrust = NormalizeTrust(rule.RequiredTrust)
					attribute(grant)
				}
				selection.adminTrustRank = maxInt(selection.adminTrustRank, adminRank)
			}
		}
	}
	return selection
}

func decideByDefault(policy *Document, reason string) Decision {
	policyVersion := policyVersionOrDefault(policy, "")
	if defaultDecisionAllow(policy) {
		return Decision{
			Allowed:       true,
			Status:        http.StatusOK,
			Reason:        reason,
			PolicyVersion: policyVersion,
		}
	}
	return Deny(http.StatusForbidden, reason, policyVersion)
}

func matchingGrants(grants []Grant, identity Identity) []Grant {
	var matched []Grant
	for _, grant := range grants {
		if subjectMatchesTeam(grant.HumanID, grant.AgentID, grant.TeamID, identity) {
			matched = append(matched, grant)
		}
	}
	return matched
}

func findSession(sessions []Binding, identity Identity) (Binding, bool) {
	if identity.SessionID != "" {
		for _, session := range sessions {
			if session.Name == identity.SessionID && subjectMatchesTeam(session.HumanID, session.AgentID, session.TeamID, identity) {
				return session, true
			}
		}
		return Binding{}, false
	}
	for _, session := range sessions {
		if subjectMatchesTeam(session.HumanID, session.AgentID, session.TeamID, identity) {
			return session, true
		}
	}
	return Binding{}, false
}

func subjectMatchesTeam(humanID HumanID, agentID AgentID, teamID TeamID, identity Identity) bool {
	if humanID != "" && humanID != identity.HumanID {
		return false
	}
	if agentID != "" && agentID != identity.AgentID {
		return false
	}
	if teamID != "" && teamID != identity.TeamID {
		return false
	}
	return humanID != "" || agentID != "" || teamID != ""
}

func resolveToolMetadata(tools []Tool, toolName ToolName) (string, string, string) {
	requiredTrust := TrustLevelLow
	for _, tool := range tools {
		if tool.Name == toolName {
			if tool.RequiredTrust != "" {
				requiredTrust = NormalizeTrust(tool.RequiredTrust)
			}
			return requiredTrust, NormalizeSideEffect(tool.SideEffect), NormalizeRiskLevel(tool.RiskLevel, requiredTrust, tool.SideEffect)
		}
	}
	return requiredTrust, "", ""
}

func NormalizeRiskLevel(risk, trust, sideEffect string) string {
	switch strings.ToLower(strings.TrimSpace(risk)) {
	case "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(risk))
	}
	trust = NormalizeTrust(trust)
	sideEffect = NormalizeSideEffect(sideEffect)
	switch {
	case sideEffect == "destructive" || trust == TrustLevelHigh:
		return "high"
	case sideEffect == "write" || trust == TrustLevelMedium:
		return "medium"
	case sideEffect == "read" && trust == TrustLevelLow:
		return "low"
	default:
		return ""
	}
}

func policyVersionOrDefault(policy *Document, def string) string {
	if policy != nil && policy.Policy != nil && policy.Policy.PolicyVersion != "" {
		return policy.Policy.PolicyVersion
	}
	return def
}

func sessionRequired(policy *Document) bool {
	return policy != nil && policy.Session != nil && policy.Session.Required
}

func policyModeObserve(policy *Document) bool {
	return policy != nil && policy.Policy != nil && strings.EqualFold(policy.Policy.Mode, "observe")
}

func defaultDecisionAllow(policy *Document) bool {
	return policy != nil && policy.Policy != nil && strings.EqualFold(policy.Policy.DefaultDecision, "allow")
}

func isExpiredAt(value string, now time.Time) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return true
	}
	return !now.Before(expiresAt)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// consideredGrants summarizes the grants a no_matching_grant denial evaluated.
func consideredGrants(policy *Document) (int, string) {
	if policy == nil {
		return 0, ""
	}
	names := make([]string, 0, maxConsideredGrantNames)
	for _, grant := range policy.Grants {
		if len(names) == maxConsideredGrantNames {
			break
		}
		names = append(names, string(grant.Name))
	}
	return len(policy.Grants), strings.Join(names, ",")
}

func ptrDecision(d Decision) *Decision { return &d }

type resolvedCaller struct {
	session         Binding
	found           bool
	name, namespace string
}

// resolveCaller applies the identity and session gates shared by every
// grant-evaluated method: tool calls and resource, prompt and skill access.
// now must be non-zero.
func resolveCaller(policy *Document, request Request, now time.Time) (resolvedCaller, *Decision) {
	identity := request.Identity
	if identity.HumanID == "" && identity.AgentID == "" && identity.TeamID == "" {
		return resolvedCaller{}, ptrDecision(Deny(http.StatusUnauthorized, "missing_identity", policyVersionOrDefault(policy, "")))
	}
	if sessionRequired(policy) && identity.SessionID == "" {
		return resolvedCaller{}, ptrDecision(Deny(http.StatusUnauthorized, "missing_session", policyVersionOrDefault(policy, "")))
	}
	session, sessionFound := findPolicySession(policy, request)
	if sessionRequired(policy) {
		if !sessionFound {
			return resolvedCaller{}, ptrDecision(Deny(http.StatusUnauthorized, "session_not_found", policyVersionOrDefault(policy, "")))
		}
		if session.Revoked {
			denied := Deny(http.StatusUnauthorized, "session_revoked", ChoosePolicyVersion(session.PolicyVersion, policyVersionOrDefault(policy, "")))
			denied.MatchedSession = string(session.Name)
			denied.MatchedSessionNamespace = string(session.Namespace)
			return resolvedCaller{}, &denied
		}
		if isExpiredAt(session.ExpiresAt, now) {
			denied := Deny(http.StatusUnauthorized, "session_expired", ChoosePolicyVersion(session.PolicyVersion, policyVersionOrDefault(policy, "")))
			denied.MatchedSession = string(session.Name)
			denied.MatchedSessionNamespace = string(session.Namespace)
			return resolvedCaller{}, &denied
		}
	} else if identity.SessionID == "" || !sessionFound || session.Revoked || isExpiredAt(session.ExpiresAt, now) {
		session = Binding{}
		sessionFound = false
	}
	caller := resolvedCaller{session: session, found: sessionFound}
	if sessionFound {
		caller.name, caller.namespace = string(session.Name), string(session.Namespace)
	}
	return caller, nil
}

// authorizePrimitive evaluates resource, prompt and skill methods. Listing needs
// any active matching grant. Reading an item also needs every matching rule of
// that kind to agree: a deny rule wins, and a grant that scopes the kind allows
// only what its allow rules match. Trust and side-effect checks are tool-only.
func authorizePrimitive(policy *Document, request Request, now time.Time) Decision {
	if now.IsZero() {
		now = time.Now()
	}
	version := policyVersionOrDefault(policy, "")
	kind, targeted, _ := PrimitiveMethod(request.RPCMethod)
	caller, denied := resolveCaller(policy, request, now)
	if denied != nil {
		return *denied
	}
	withSession := func(d Decision) Decision {
		d.MatchedSession, d.MatchedSessionNamespace = caller.name, caller.namespace
		return d
	}
	if targeted && strings.TrimSpace(request.Target) == "" {
		return withSession(Deny(http.StatusForbidden, "primitive_target_missing", version))
	}
	grants := matchingPolicyGrants(policy, request.Identity)
	if len(grants) == 0 {
		d := decideByDefault(policy, "no_matching_grant")
		d.ConsideredGrantCount, d.ConsideredGrantNames = consideredGrants(policy)
		return withSession(d)
	}
	sort.Slice(grants, func(i, j int) bool {
		return string(grants[i].Namespace)+"\x00"+grants[i].Name < string(grants[j].Namespace)+"\x00"+grants[j].Name
	})
	var active []Grant
	var expired int
	for _, grant := range grants {
		switch {
		case grant.Disabled:
		case isExpiredAt(grant.ExpiresAt, now):
			expired++
		default:
			active = append(active, grant)
		}
	}
	notGranted := func() Decision {
		reason := "primitive_not_granted"
		if expired > 0 && len(active) == 0 {
			reason = "grant_expired"
		}
		return decideByDefault(policy, reason)
	}
	if len(active) == 0 {
		return withSession(notGranted())
	}
	targets := request.Targets
	if targeted {
		targets = []string{request.Target}
	}
	allowedBy := &active[0]
	for _, target := range targets {
		grant, denial := grantForTarget(active, kind, target, version)
		if denial != nil {
			return withSession(*denial)
		}
		if grant == nil {
			return withSession(notGranted())
		}
		allowedBy = grant
	}
	return withSession(Decision{
		Allowed: true, Status: http.StatusOK, Reason: "allowed",
		PolicyVersion:         ChoosePolicyVersion(allowedBy.PolicyVersion, version),
		MatchedGrant:          allowedBy.Name,
		MatchedGrantNamespace: string(allowedBy.Namespace),
	})
}

// grantForTarget returns the first active grant that allows target, or a denial
// when a matching deny rule applies to any grant. A grant with no rule of the
// kind allows everything of that kind. A nil grant and nil denial mean no grant
// allowed it.
func grantForTarget(active []Grant, kind, target, version string) (*Grant, *Decision) {
	var allowedBy *Grant
	for i := range active {
		grant := active[i]
		scoped, matchedAllow := false, false
		for _, rule := range grant.PrimitiveRules {
			if rule.Kind != kind {
				continue
			}
			scoped = true
			if !primitivePatternMatches(rule.Pattern, target) {
				continue
			}
			if strings.EqualFold(rule.Decision, "deny") {
				d := Deny(http.StatusForbidden, "primitive_denied", ChoosePolicyVersion(grant.PolicyVersion, version))
				d.MatchedGrant, d.MatchedGrantNamespace = grant.Name, string(grant.Namespace)
				return nil, &d
			}
			matchedAllow = true
		}
		if (!scoped || matchedAllow) && allowedBy == nil {
			allowedBy = &active[i]
		}
	}
	return allowedBy, nil
}

// primitivePatternMatches reports an exact match, or a prefix match when the
// pattern ends in "*".
func primitivePatternMatches(pattern, target string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(target, prefix)
	}
	return pattern == target
}
