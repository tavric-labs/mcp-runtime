package update

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"mcp-runtime/internal/platformrelease"
)

var tokenKeyDeployments = map[string]string{
	"mcp-platform-api":  "mcp-platform",
	"mcp-runtime-api":   "mcp-platform",
	"mcp-analytics-api": "mcp-observability",
}

// legacyJWTSecretEnv is the v0.14.1 shape: the shared JWT_SECRET from the
// service's own credentials Secret, and none of the ES256 key env.
func legacyJWTSecretEnv(secret string) corev1.EnvVar {
	return corev1.EnvVar{Name: "JWT_SECRET", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: secret}, Key: "JWT_SECRET", Optional: boolPtr(true)}}}
}

func boolPtr(b bool) *bool { return &b }

var legacySecretNames = map[string]string{
	"mcp-platform-api":  "mcp-platform-api-credentials",
	"mcp-runtime-api":   "mcp-runtime-api-credentials",
	"mcp-analytics-api": "mcp-analytics-api-credentials",
}

// installedLegacyTokenEnv is installed(version) whose three token key consumers
// carry JWT_SECRET and an unrelated KEEP env that update must not touch.
func installedLegacyTokenEnv(version string) []runtime.Object {
	objs := installed(version)
	for _, o := range objs {
		if d, ok := o.(*appsv1.Deployment); ok && tokenKeyDeployments[d.Name] != "" {
			d.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{
				{Name: "KEEP", Value: "me"},
				legacyJWTSecretEnv(legacySecretNames[d.Name]),
			}
		}
	}
	return objs
}

// tokenKeyManifest is the v0.5.0 manifest carrying the repository's real wiring
// for the three token key consumers.
func tokenKeyManifest(t *testing.T) *platformrelease.Manifest {
	t.Helper()
	m := manifest(t, "v0.5.0")
	wiring, err := platformrelease.LoadWiring("../../../config/release/wiring.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pick := map[string]*platformrelease.ComponentWiring{}
	for _, name := range []string{"platform-api", "runtime-api", "analytics-api"} {
		pick[name] = wiring.Components[name]
	}
	if err := m.ApplyReleaseExtras(platformrelease.ReleaseExtras{Wiring: pick}); err != nil {
		t.Fatal(err)
	}
	return m
}

func deploymentNamed(t *testing.T, cs *fake.Clientset, name string) *appsv1.Deployment {
	t.Helper()
	d, err := cs.AppsV1().Deployments(tokenKeyDeployments[name]).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func envNames(d *appsv1.Deployment) []string {
	var out []string
	for _, e := range d.Spec.Template.Spec.Containers[0].Env {
		out = append(out, e.Name)
	}
	return out
}

func envByName(d *appsv1.Deployment, name string) *corev1.EnvVar {
	for i := range d.Spec.Template.Spec.Containers[0].Env {
		if e := &d.Spec.Template.Spec.Containers[0].Env[i]; e.Name == name {
			return e
		}
	}
	return nil
}

func assertSecretRef(t *testing.T, d *appsv1.Deployment, name, secret, key string, optional bool) {
	t.Helper()
	e := envByName(d, name)
	if e == nil || e.Value != "" || e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("%s: env %s is not a secretKeyRef: %+v (env %v)", d.Name, name, e, envNames(d))
	}
	ref := e.ValueFrom.SecretKeyRef
	gotOptional := ref.Optional != nil && *ref.Optional
	if ref.Name != secret || ref.Key != key || gotOptional != optional {
		t.Fatalf("%s: %s ref = %s/%s optional=%v, want %s/%s optional=%v", d.Name, name, ref.Name, ref.Key, gotOptional, secret, key, optional)
	}
}

func assertTokenKeyEnvWired(t *testing.T, cs *fake.Clientset) {
	t.Helper()
	pa := deploymentNamed(t, cs, "mcp-platform-api")
	assertSecretRef(t, pa, "JWT_SIGNING_KEY", "mcp-platform-api-credentials", "JWT_SIGNING_KEY", true)
	if envByName(pa, "JWT_VERIFY_KEY") != nil {
		t.Fatal("platform-api must not get the verify key env")
	}
	assertSecretRef(t, deploymentNamed(t, cs, "mcp-runtime-api"), "JWT_VERIFY_KEY", "mcp-runtime-api-credentials", "JWT_VERIFY_KEY", false)
	assertSecretRef(t, deploymentNamed(t, cs, "mcp-analytics-api"), "JWT_VERIFY_KEY", "mcp-analytics-api-credentials", "JWT_VERIFY_KEY", false)
	for name := range tokenKeyDeployments {
		d := deploymentNamed(t, cs, name)
		if envByName(d, "JWT_SECRET") != nil {
			t.Fatalf("%s still has JWT_SECRET: %v", name, envNames(d))
		}
		if e := envByName(d, "KEEP"); e == nil || e.Value != "me" {
			t.Fatalf("%s lost unrelated env: %v", name, envNames(d))
		}
	}
}

func runTokenKeyUpdate(t *testing.T, cs *fake.Clientset, m *platformrelease.Manifest, args ...string) (string, error) {
	t.Helper()
	d := testDeps(cs, m, nil)
	return execute(t, d, append([]string{"--to", m.Version}, args...)...)
}

func TestUpdateWiresTokenKeyEnvAndRetiresJWTSecret(t *testing.T) {
	cs := fake.NewSimpleClientset(installedLegacyTokenEnv("v0.4.0")...)
	if out, err := runTokenKeyUpdate(t, cs, tokenKeyManifest(t), "--yes"); err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	assertTokenKeyEnvWired(t, cs)
}

func TestUpdateWiresTokenKeyEnvAfterSecretsAndVerifiersFirst(t *testing.T) {
	cs := fake.NewSimpleClientset(installedLegacyTokenEnv("v0.4.0")...)
	if out, err := runTokenKeyUpdate(t, cs, tokenKeyManifest(t), "--yes"); err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	lastSecret, first := -1, map[string]int{}
	for i, a := range cs.Actions() {
		switch {
		case a.GetResource().Resource == "secrets" && a.GetVerb() != "get" && a.GetVerb() != "list":
			lastSecret = i
		case a.GetResource().Resource == "deployments" && a.GetVerb() == "patch":
			name := a.(k8stesting.PatchAction).GetName()
			if _, ok := first[name]; !ok {
				first[name] = i
			}
		}
	}
	for name, i := range first {
		if lastSecret < 0 || i < lastSecret {
			t.Fatalf("%s was patched (action %d) before the key Secrets were written (%d)", name, i, lastSecret)
		}
	}
	if first["mcp-platform-api"] < first["mcp-runtime-api"] || first["mcp-platform-api"] < first["mcp-analytics-api"] {
		t.Fatalf("platform-api must roll after the verifiers: %v", first)
	}
}

func TestUpdateTokenKeyEnvIsIdempotent(t *testing.T) {
	cs := fake.NewSimpleClientset(installedLegacyTokenEnv("v0.4.0")...)
	m := tokenKeyManifest(t)
	if out, err := runTokenKeyUpdate(t, cs, m, "--yes"); err != nil {
		t.Fatalf("first run: %v\n%s", err, out)
	}
	cs.ClearActions()
	out, err := runTokenKeyUpdate(t, cs, m, "--yes")
	if err != nil {
		t.Fatalf("second run: %v\n%s", err, out)
	}
	if p := patchedDeployments(cs); len(p) != 0 {
		t.Fatalf("second run patched %v", p)
	}
	assertTokenKeyEnvWired(t, cs)
}

func TestUpdateWiresTokenKeyEnvWhenImagesAlreadyCurrent(t *testing.T) {
	cs := fake.NewSimpleClientset(installedLegacyTokenEnv("v0.5.0")...)
	plan, err := BuildPlan(context.Background(), cs, tokenKeyManifest(t), Selection{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"platform-api", "runtime-api", "analytics-api"} {
		row := rowFor(t, plan, c)
		joined := strings.Join(row.Wiring, "; ")
		if row.Action != ActionUpdate || !strings.Contains(joined, "JWT_SECRET removed") {
			t.Fatalf("%s: want a wiring-only update retiring JWT_SECRET, got %s %v", c, row.Action, row.Wiring)
		}
	}
	if !strings.Contains(strings.Join(rowFor(t, plan, "platform-api").Wiring, ";"), "JWT_SIGNING_KEY") ||
		!strings.Contains(strings.Join(rowFor(t, plan, "runtime-api").Wiring, ";"), "JWT_VERIFY_KEY") {
		t.Fatalf("drift must name the added key env: %+v", plan.Rows)
	}
}

func TestUpdateDryRunReportsTokenKeyEnvWithoutWriting(t *testing.T) {
	cs := fake.NewSimpleClientset(installedLegacyTokenEnv("v0.4.0")...)
	out, err := runTokenKeyUpdate(t, cs, tokenKeyManifest(t), "--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v\n%s", err, out)
	}
	for _, want := range []string{"JWT_SIGNING_KEY", "JWT_VERIFY_KEY", "JWT_SECRET removed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output lacks %q:\n%s", want, out)
		}
	}
	if w := secretWrites(cs); len(w) != 0 || len(patchedDeployments(cs)) != 0 {
		t.Fatalf("dry-run wrote: %v %v", w, patchedDeployments(cs))
	}
	if envByName(deploymentNamed(t, cs, "mcp-platform-api"), "JWT_SECRET") == nil {
		t.Fatal("dry-run removed JWT_SECRET")
	}
}

func TestRollbackRestoresLegacyJWTSecretEnv(t *testing.T) {
	cs := fake.NewSimpleClientset(installedLegacyTokenEnv("v0.4.0")...)
	plan, err := BuildPlan(context.Background(), cs, tokenKeyManifest(t), Selection{})
	if err != nil {
		t.Fatal(err)
	}
	waiter := func(_ context.Context, _, name string, _ time.Duration) error {
		if name == "mcp-platform-api" && !strings.Contains(deploymentNamed(t, cs, name).Spec.Template.Spec.Containers[0].Image, "v0.4.0") {
			return errors.New("platform-api not ready")
		}
		return nil
	}
	res := Apply(context.Background(), cs, plan, ApplyOptions{Timeout: time.Second, RollbackOnFailure: true, Waiter: waiter})
	if !res.Failed {
		t.Fatal("expected the failed platform-api rollout to fail the update")
	}
	for name, secret := range legacySecretNames {
		d := deploymentNamed(t, cs, name)
		if envByName(d, "JWT_SIGNING_KEY") != nil || envByName(d, "JWT_VERIFY_KEY") != nil {
			t.Fatalf("%s keeps the new key env after rollback: %v", name, envNames(d))
		}
		legacy := envByName(d, "JWT_SECRET")
		if legacy == nil || legacy.ValueFrom == nil || legacy.ValueFrom.SecretKeyRef == nil ||
			legacy.ValueFrom.SecretKeyRef.Name != secret || legacy.ValueFrom.SecretKeyRef.Key != "JWT_SECRET" {
			t.Fatalf("%s: JWT_SECRET not restored as the original secretKeyRef: %+v", name, legacy)
		}
		if e := envByName(d, "KEEP"); e == nil || e.Value != "me" {
			t.Fatalf("%s lost unrelated env on rollback", name)
		}
		if !strings.Contains(d.Spec.Template.Spec.Containers[0].Image, "v0.4.0") {
			t.Fatalf("%s image not restored: %s", name, d.Spec.Template.Spec.Containers[0].Image)
		}
	}
}

func TestSecretEnvReplacesLiteralAndRollbackRestoresIt(t *testing.T) {
	objs := installed("v0.4.0")
	for _, o := range objs {
		if d, ok := o.(*appsv1.Deployment); ok && d.Name == "mcp-platform-api" {
			d.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "JWT_SIGNING_KEY", Value: "literal"}}
		}
	}
	cs := fake.NewSimpleClientset(objs...)
	plan, err := BuildPlan(context.Background(), cs, tokenKeyManifest(t), Selection{})
	if err != nil {
		t.Fatal(err)
	}
	failing := func(_ context.Context, _, name string, _ time.Duration) error {
		if name == "mcp-platform-api" && strings.Contains(deploymentNamed(t, cs, name).Spec.Template.Spec.Containers[0].Image, "v0.5.0") {
			return errors.New("not ready")
		}
		return nil
	}
	// Apply then roll back: first confirm the literal became a reference.
	res := Apply(context.Background(), cs, plan, ApplyOptions{Timeout: time.Second, RollbackOnFailure: false, Waiter: failing})
	if !res.Failed {
		t.Fatal("expected failure")
	}
	assertSecretRef(t, deploymentNamed(t, cs, "mcp-platform-api"), "JWT_SIGNING_KEY", "mcp-platform-api-credentials", "JWT_SIGNING_KEY", true)

	cs2 := fake.NewSimpleClientset(objs...)
	plan2, _ := BuildPlan(context.Background(), cs2, tokenKeyManifest(t), Selection{})
	failing2 := func(_ context.Context, _, name string, _ time.Duration) error {
		if name == "mcp-platform-api" && strings.Contains(deploymentNamed(t, cs2, name).Spec.Template.Spec.Containers[0].Image, "v0.5.0") {
			return errors.New("not ready")
		}
		return nil
	}
	if res := Apply(context.Background(), cs2, plan2, ApplyOptions{Timeout: time.Second, RollbackOnFailure: true, Waiter: failing2}); !res.Failed {
		t.Fatal("expected failure")
	}
	e := envByName(deploymentNamed(t, cs2, "mcp-platform-api"), "JWT_SIGNING_KEY")
	if e == nil || e.Value != "literal" || e.ValueFrom != nil {
		t.Fatalf("literal not restored on rollback: %+v", e)
	}
}

func TestRecoveryCommandsRestoreRetiredEnv(t *testing.T) {
	cs := fake.NewSimpleClientset(installedLegacyTokenEnv("v0.4.0")...)
	plan, err := BuildPlan(context.Background(), cs, tokenKeyManifest(t), Selection{})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, w := range groupByWorkload(plan.Changed()) {
		if w.Deployment != "mcp-platform-api" {
			continue
		}
		cmds := strings.Join(recoveryCommands(w), "\n")
		if !strings.Contains(cmds, "JWT_SIGNING_KEY-") || !strings.Contains(cmds, "JWT_SECRET") {
			t.Fatalf("recovery commands must unset the new env and restore JWT_SECRET:\n%s", cmds)
		}
		found = true
	}
	if !found {
		t.Fatal("platform-api workload missing")
	}
}
