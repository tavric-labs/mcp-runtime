package platformapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"mcp-runtime/pkg/apikit"
	"mcp-runtime/pkg/openapi"
	"mcp-runtime/pkg/sessionstate"
)

// The services share a network contract, not code: platform-api publishes
// openapi.yaml and this client may only call operations that document lists.
// A client call with no matching operation fails here, so the two services
// cannot drift apart without CI noticing.

type recordedCall struct{ method, path string }

func loadPlatformAPISpec(t *testing.T) *openapi3.T {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "platform-api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read platform-api openapi.yaml: %v", err)
	}
	doc, err := openapi.Load(data)
	if err != nil {
		t.Fatalf("load platform-api openapi.yaml: %v", err)
	}
	return doc
}

// operationExists reports whether the spec documents method on a path that
// matches the concrete request path.
func operationExists(doc *openapi3.T, method, path string) bool {
	for template, item := range doc.Paths.Map() {
		pattern := "^" + regexp.MustCompile(`\\\{[^}]+\\\}`).ReplaceAllString(regexp.QuoteMeta(template), `[^/]+`) + "$"
		if !regexp.MustCompile(pattern).MatchString(path) {
			continue
		}
		if item.GetOperation(method) != nil {
			return true
		}
	}
	return false
}

func TestClientCallsOnlyDocumentedPlatformAPIOperations(t *testing.T) {
	doc := loadPlatformAPISpec(t)

	var mu sync.Mutex
	var calls []recordedCall
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, recordedCall{r.Method, r.URL.Path})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	c := &Client{BaseURL: server.URL, Token: "internal-token"}
	ctx := context.Background()
	filter := sessionstate.Filter{GrantNamespace: "ns", GrantName: "grant"}

	// Every exported operation of the client appears here once. A new client
	// method that is not listed is caught by TestContractCoversEveryClientOperation.
	operations := map[string]func(){
		"ListTeams":                 func() { _, _ = c.ListTeams(ctx) },
		"GetTeamBySlug":             func() { _, _, _ = c.GetTeamBySlug(ctx, "team") },
		"CreateTeam":                func() { _, _ = c.CreateTeam(ctx, "team", "Team", "user") },
		"DeleteTeamBySlug":          func() { _ = c.DeleteTeamBySlug(ctx, "team") },
		"CreateAgent":               func() { _, _ = c.CreateAgent(ctx, "team", "agent", "user") },
		"ListAgents":                func() { _, _ = c.ListAgents(ctx, "team", "", "", "", 10) },
		"AgentCertificates":         func() { _, _ = c.AgentCertificates(ctx, "agent", "human") },
		"GetAgent":                  func() { _, _, _ = c.GetAgent(ctx, "agent") },
		"RenameAgent":               func() { _, _ = c.RenameAgent(ctx, "agent", "renamed") },
		"SetAgentStatus":            func() { _, _ = c.SetAgentStatus(ctx, "agent", "inactive", "actor") },
		"PrincipalForUserID":        func() { _, _ = c.PrincipalForUserID(ctx, "user") },
		"ListNamespaces":            func() { _, _ = c.ListNamespaces(ctx) },
		"GetNamespace":              func() { _, _, _ = c.GetNamespace(ctx, "ns") },
		"ListTeamMemberships":       func() { _, _ = c.ListTeamMemberships(ctx, "team") },
		"UpsertTeamMembership":      func() { _, _ = c.UpsertTeamMembership(ctx, "team", "user", "member") },
		"DeleteTeamMembership":      func() { _ = c.DeleteTeamMembership(ctx, "team", "user") },
		"CreatePasswordUser":        func() { _, _ = c.CreatePasswordUser(ctx, "a@example.com", "pw", "user") },
		"CreateTeamUser":            func() { _, _, _ = c.CreateTeamUser(ctx, "team", "a@example.com", "pw", "member") },
		"WriteAudit":                func() { c.WriteAudit(ctx, AuditEvent{Action: "a", Resource: "r", Status: "success"}) },
		"OperationsSnapshot":        func() { _, _ = c.OperationsSnapshot(ctx, OperationsFilter{}) },
		"OverviewCounts":            func() { _, _ = c.OverviewCounts(ctx) },
		"PutSession":                func() { _, _ = c.PutSession(ctx, sessionstate.Session{ID: "ns/s", Namespace: "ns"}) },
		"GetSession":                func() { _, _, _ = c.GetSession(ctx, "ns", "s") },
		"ListSessions":              func() { _, _ = c.ListSessions(ctx, "ns") },
		"ListSessionsFiltered":      func() { _, _ = c.ListSessionsFiltered(ctx, filter) },
		"SetSessionRevoked":         func() { _ = c.SetSessionRevoked(ctx, "ns", "s", true, "cause") },
		"RevokeSession":             func() { _ = c.RevokeSession(ctx, "ns", "s", "cause") },
		"DeleteSession":             func() { _ = c.DeleteSession(ctx, "ns", "s") },
		"CompleteSessionEnrollment": func() { _ = c.CompleteSessionEnrollment(ctx, "ns", "s", "uid") },
		"DeletePendingSession":      func() { _ = c.DeletePendingSession(ctx, "ns", "s", "uid", "human") },
		"DeleteSessionIfUID":        func() { _ = c.DeleteSessionIfUID(ctx, "ns", "s", "uid") },
		"RevokeSessions":            func() { _, _ = c.RevokeSessions(ctx, filter, "cause") },
		"ListTeamIntents":      func() { _, _ = c.ListTeamIntents(ctx) },
		"GetTeamIntent":        func() { _, _, _ = c.GetTeamIntent(ctx, "team-1") },
		"ClaimTeamIntentFence": func() { _, _ = c.ClaimTeamIntentFence(ctx, "team-1", "worker-1") },
		"RecordTeamIntentReceipt": func() {
			_, _ = c.RecordTeamIntentReceipt(ctx, "team-1", apikit.TeamIntentReceiptRequest{DesiredRevisionID: "rev-1", FenceEpoch: 1, Outcome: apikit.TeamIntentOutcomeApplied, WorkerID: "worker-1"})
		},
		"ClaimTeamIntentObserverLease": func() { _, _ = c.ClaimTeamIntentObserverLease(ctx, "default", "observer-1") },
		"RecordTeamIntentObservation": func() {
			_, _ = c.RecordTeamIntentObservation(ctx, "team-1", apikit.TeamIntentObservationRequest{ObserverID: "observer-1", ObserverEpoch: 1, Health: apikit.TeamIntentHealthReady})
		},

		"ClaimWorkloadObserverLease": func() { _, _ = c.ClaimWorkloadObserverLease(ctx, "default", "obs-1") },
		"ListServerIntents":          func() { _, _ = c.ListServerIntents(ctx) },
		"UpsertServerIntent": func() {
			_, _ = c.UpsertServerIntent(ctx, apikit.ServerIntentUpsertRequest{ServerID: "srv-1", Namespace: "ns", Name: "app"})
		},
		"GetServerIntent": func() { _, _, _ = c.GetServerIntent(ctx, "srv-1") },
		"RetireServerIntent": func() {
			_, _ = c.RetireServerIntent(ctx, "srv-1", apikit.ServerIntentRetireRequest{TargetCRUID: "uid-1"})
		},
		"ClaimServerIntentFence": func() { _, _ = c.ClaimServerIntentFence(ctx, "srv-1", "w-1") },
		"RecordServerRetirementCheckpoint": func() {
			_, _ = c.RecordServerRetirementCheckpoint(ctx, "srv-1", apikit.ServerRetirementCheckpointRequest{DesiredRevisionID: "r1", FenceEpoch: 1, WorkerID: "w-1"})
		},
		"RecordServerIntentReceipt": func() {
			_, _ = c.RecordServerIntentReceipt(ctx, "srv-1", apikit.ServerIntentReceiptRequest{DesiredRevisionID: "r1", FenceEpoch: 1, Outcome: apikit.WorkloadIntentOutcomeApplied, WorkerID: "w-1"})
		},
		"RecordServerIntentObservation": func() {
			_, _ = c.RecordServerIntentObservation(ctx, "srv-1", apikit.ServerIntentObservationRequest{ObserverID: "obs-1", ObserverEpoch: 1, Health: apikit.WorkloadIntentHealthReady})
		},
		"ListDeploymentIntents": func() { _, _ = c.ListDeploymentIntents(ctx) },
		"UpsertDeploymentIntent": func() {
			_, _ = c.UpsertDeploymentIntent(ctx, apikit.DeploymentIntentUpsertRequest{DeploymentID: "dep-1", Namespace: "ns", Name: "app", Image: "img:v1"})
		},
		"GetDeploymentIntent": func() { _, _, _ = c.GetDeploymentIntent(ctx, "dep-1") },
		"DeleteDeploymentIntent": func() {
			_, _ = c.DeleteDeploymentIntent(ctx, "dep-1", apikit.DeploymentIntentDeleteRequest{TargetDeploymentUID: "uid-1"})
		},
		"ClaimDeploymentIntentFence": func() { _, _ = c.ClaimDeploymentIntentFence(ctx, "dep-1", "w-1") },
		"RecordDeploymentIntentReceipt": func() {
			_, _ = c.RecordDeploymentIntentReceipt(ctx, "dep-1", apikit.DeploymentIntentReceiptRequest{DesiredRevisionID: "r1", FenceEpoch: 1, Outcome: apikit.WorkloadIntentOutcomeApplied, WorkerID: "w-1"})
		},
		"RecordDeploymentIntentObservation": func() {
			_, _ = c.RecordDeploymentIntentObservation(ctx, "dep-1", apikit.DeploymentIntentObservationRequest{ObserverID: "obs-1", ObserverEpoch: 1, Health: apikit.WorkloadIntentHealthReady})
		},
		"ListImagePublicationIntents": func() { _, _ = c.ListImagePublicationIntents(ctx) },
		"BeginImagePublication": func() {
			_, _ = c.BeginImagePublication(ctx, apikit.ImagePublicationBeginRequest{PublicationID: "pub-1", Namespace: "ns", Registry: "r", Repository: "repo", Tag: "v1"})
		},
		"GetImagePublicationIntent": func() { _, _, _ = c.GetImagePublicationIntent(ctx, "pub-1") },
		"FinalizeImagePublication": func() {
			_, _ = c.FinalizeImagePublication(ctx, "pub-1", apikit.ImagePublicationFinalizeRequest{ArtifactRef: "finalized/pub-1/sha.tar", ArtifactSHA256: "abc", ArtifactSizeBytes: 10})
		},
		"AbortImagePublication":      func() { _, _ = c.AbortImagePublication(ctx, "pub-1", apikit.ImagePublicationAbortRequest{}) },
		"ClaimImagePublicationFence": func() { _, _ = c.ClaimImagePublicationFence(ctx, "pub-1", "w-1") },
		"RecordImagePublicationReceipt": func() {
			_, _ = c.RecordImagePublicationReceipt(ctx, "pub-1", apikit.ImagePublicationReceiptRequest{DesiredRevisionID: "r1", FenceEpoch: 1, Outcome: apikit.WorkloadIntentOutcomeApplied, VerifiedDigest: "sha256:abc", WorkerID: "w-1"})
		},
		"ListRestartOperationIntents": func() { _, _ = c.ListRestartOperationIntents(ctx) },
		"CreateRestartOperation": func() {
			_, _ = c.CreateRestartOperation(ctx, apikit.RestartOperationCreateRequest{OperationID: "op-1", RequestedBy: "admin", TargetComponents: []string{"gateway"}})
		},
		"GetRestartOperationIntent":  func() { _, _, _ = c.GetRestartOperationIntent(ctx, "op-1") },
		"ClaimRestartOperationFence": func() { _, _ = c.ClaimRestartOperationFence(ctx, "op-1", "w-1") },
		"RecordRestartComponentCheckpoint": func() {
			_, _ = c.RecordRestartComponentCheckpoint(ctx, "op-1", apikit.RestartComponentCheckpointRequest{DesiredRevisionID: "r1", FenceEpoch: 1, WorkerID: "w-1", Checkpoint: apikit.ComponentRestartCheckpoint{Component: "gateway", Completed: true}})
		},
		"RecordRestartOperationReceipt": func() {
			_, _ = c.RecordRestartOperationReceipt(ctx, "op-1", apikit.RestartOperationReceiptRequest{DesiredRevisionID: "r1", FenceEpoch: 1, Outcome: apikit.WorkloadIntentOutcomeApplied, WorkerID: "w-1"})
		},
	}

	names := make([]string, 0, len(operations))
	for name := range operations {
		names = append(names, name)
	}
	sort.Strings(names)

	var undocumented []string
	for _, name := range names {
		mu.Lock()
		calls = nil
		mu.Unlock()
		operations[name]()
		mu.Lock()
		got := append([]recordedCall(nil), calls...)
		mu.Unlock()
		if len(got) == 0 {
			t.Errorf("%s made no request", name)
		}
		for _, call := range got {
			if !strings.HasPrefix(call.path, "/internal/") {
				t.Errorf("%s calls %s %s: service calls must use /internal/", name, call.method, call.path)
			}
			if !operationExists(doc, call.method, call.path) {
				undocumented = append(undocumented, name+": "+call.method+" "+call.path)
			}
		}
	}
	for _, entry := range undocumented {
		t.Errorf("client calls an operation platform-api's openapi.yaml does not document: %s", entry)
	}
}

// TestContractCoversEveryClientOperation fails when someone adds an exported
// Client method without adding it to the contract test above.
func TestContractCoversEveryClientOperation(t *testing.T) {
	covered := map[string]bool{}
	for _, name := range strings.Fields(`ListTeams GetTeamBySlug CreateTeam DeleteTeamBySlug CreateAgent ListAgents
		AgentCertificates GetAgent RenameAgent SetAgentStatus PrincipalForUserID ListNamespaces GetNamespace
		ListTeamMemberships UpsertTeamMembership DeleteTeamMembership CreatePasswordUser CreateTeamUser WriteAudit
		OperationsSnapshot OverviewCounts PutSession GetSession ListSessions ListSessionsFiltered SetSessionRevoked
		RevokeSession DeleteSession CompleteSessionEnrollment DeletePendingSession DeleteSessionIfUID RevokeSessions
		ListTeamIntents GetTeamIntent ClaimTeamIntentFence RecordTeamIntentReceipt ClaimTeamIntentObserverLease
		RecordTeamIntentObservation
		ClaimWorkloadObserverLease
		ListServerIntents UpsertServerIntent GetServerIntent RetireServerIntent ClaimServerIntentFence RecordServerRetirementCheckpoint RecordServerIntentReceipt RecordServerIntentObservation
		ListDeploymentIntents UpsertDeploymentIntent GetDeploymentIntent DeleteDeploymentIntent ClaimDeploymentIntentFence RecordDeploymentIntentReceipt RecordDeploymentIntentObservation
		ListImagePublicationIntents BeginImagePublication GetImagePublicationIntent FinalizeImagePublication AbortImagePublication ClaimImagePublicationFence RecordImagePublicationReceipt
		ListRestartOperationIntents CreateRestartOperation GetRestartOperationIntent ClaimRestartOperationFence RecordRestartComponentCheckpoint RecordRestartOperationReceipt
		Configured`) {
		covered[name] = true
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	method := regexp.MustCompile(`(?m)^func \(c \*Client\) ([A-Z]\w*)\(`)
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range method.FindAllStringSubmatch(string(src), -1) {
			if !covered[match[1]] {
				t.Errorf("Client.%s is not covered by the contract test; add it to the operations table", match[1])
			}
		}
	}
}
