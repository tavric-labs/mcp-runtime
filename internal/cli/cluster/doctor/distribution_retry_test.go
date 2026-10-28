package doctor

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mcp-runtime/internal/cli/core"
)

// nodeLookups scripts `get nodes` answers and records every node-lookup call.
type nodeLookups struct {
	mu      sync.Mutex
	kubelet []mockAnswer // consumed in order; the last repeats
	names   []mockAnswer
	calls   []string
}

type mockAnswer struct {
	out string
	err error
}

func (n *nodeLookups) executor() *core.MockExecutor {
	next := func(answers []mockAnswer, used int) mockAnswer {
		if len(answers) == 0 {
			return mockAnswer{}
		}
		if used >= len(answers) {
			return answers[len(answers)-1]
		}
		return answers[used]
	}
	kubeletUsed, namesUsed := 0, 0
	return &core.MockExecutor{CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
		n.mu.Lock()
		defer n.mu.Unlock()
		switch {
		case contains(spec.Args, "current-context"):
			return &core.MockCommand{OutputData: []byte("production")}
		case contains(spec.Args, "jsonpath={.items[*].status.nodeInfo.kubeletVersion}"):
			n.calls = append(n.calls, strings.Join(spec.Args, " "))
			a := next(n.kubelet, kubeletUsed)
			kubeletUsed++
			return &core.MockCommand{OutputData: []byte(a.out), OutputErr: a.err}
		case contains(spec.Args, "jsonpath={.items[*].metadata.name}"):
			n.calls = append(n.calls, strings.Join(spec.Args, " "))
			a := next(n.names, namesUsed)
			namesUsed++
			return &core.MockCommand{OutputData: []byte(a.out), OutputErr: a.err}
		}
		return &core.MockCommand{}
	}}
}

func TestDetectDistributionRetriesATransientNodeLookupFailure(t *testing.T) {
	lookups := &nodeLookups{kubelet: []mockAnswer{
		{err: errors.New("context deadline exceeded")},
		{out: "v1.36.4+k3s1"},
	}}
	got := DetectDistribution(core.NewTestKubectlClient(lookups.executor()))
	if got != DistroK3s {
		t.Fatalf("DetectDistribution() = %q after one transient failure, want %q", got, DistroK3s)
	}
}

func TestDetectDistributionLetsNodeLookupsTakeLongerThanTwoSeconds(t *testing.T) {
	lookups := &nodeLookups{kubelet: []mockAnswer{{out: "v1.36.4+k3s1"}}}
	DetectDistribution(core.NewTestKubectlClient(lookups.executor()))
	if len(lookups.calls) == 0 {
		t.Fatal("no node lookup was made")
	}
	var timeout time.Duration
	for _, arg := range strings.Fields(lookups.calls[0]) {
		if value, ok := strings.CutPrefix(arg, "--request-timeout="); ok {
			parsed, err := time.ParseDuration(value)
			if err != nil {
				t.Fatalf("bad --request-timeout %q: %v", value, err)
			}
			timeout = parsed
		}
	}
	if timeout < 5*time.Second {
		t.Fatalf("--request-timeout = %s; a slow API round-trip from a remote runner must not read as a non-k3s cluster", timeout)
	}
}

func TestDetectDistributionDoesNotRetryAnAnsweredLookup(t *testing.T) {
	lookups := &nodeLookups{kubelet: []mockAnswer{{out: "v1.34.0"}}, names: []mockAnswer{{out: "kind-control-plane"}}}
	if got := DetectDistribution(core.NewTestKubectlClient(lookups.executor())); got != DistroKind {
		t.Fatalf("DetectDistribution() = %q, want %q", got, DistroKind)
	}
	kubeletCalls := 0
	for _, call := range lookups.calls {
		if strings.Contains(call, "kubeletVersion") {
			kubeletCalls++
		}
	}
	if kubeletCalls != 1 {
		t.Fatalf("kubelet lookup ran %d times, want 1: a clean answer is not retried", kubeletCalls)
	}
}

func TestDetectDistributionGivesUpQuicklyWhenTheAPIIsDown(t *testing.T) {
	down := errors.New("connection refused")
	lookups := &nodeLookups{kubelet: []mockAnswer{{err: down}}, names: []mockAnswer{{err: down}}}
	if got := DetectDistribution(core.NewTestKubectlClient(lookups.executor())); got != DistroGeneric {
		t.Fatalf("DetectDistribution() = %q with the API down, want %q", got, DistroGeneric)
	}
	if len(lookups.calls) > 4 {
		t.Fatalf("made %d node lookups against a down API (%s), want a small bounded number", len(lookups.calls), strconv.Itoa(len(lookups.calls)))
	}
}
