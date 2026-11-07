package update

import (
	"context"
	"io"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"mcp-runtime/pkg/passport"
)

const (
	platformSecretNS  = "mcp-platform"
	analyticsSecretNS = "mcp-observability"
)

func credentialSecret(ns, name string, data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

// legacySecrets is a v0.14.1 install: only the shared JWT_SECRET.
func legacySecrets() []runtime.Object {
	legacy := map[string]string{"JWT_SECRET": "legacy-shared-secret", "API_KEYS": "k"}
	return []runtime.Object{
		credentialSecret(platformSecretNS, "mcp-platform-api-credentials", legacy),
		credentialSecret(platformSecretNS, "mcp-runtime-api-credentials", legacy),
		credentialSecret(analyticsSecretNS, "mcp-analytics-api-credentials", legacy),
	}
}

func secretData(t *testing.T, cs *fake.Clientset, ns, name string) map[string]string {
	t.Helper()
	s, err := cs.CoreV1().Secrets(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for k, v := range s.Data {
		out[k] = string(v)
	}
	return out
}

func tokenKeyCluster() *fake.Clientset { return fake.NewSimpleClientset(installed("v0.4.0")...) }

// installedWithoutSecrets is an install whose credential Secrets are supplied by the test.
func installedWithoutSecrets() []runtime.Object {
	var out []runtime.Object
	for _, o := range installed("v0.4.0") {
		if _, ok := o.(*corev1.Secret); !ok {
			out = append(out, o)
		}
	}
	return out
}

func secretWrites(cs *fake.Clientset) []string {
	var out []string
	for _, a := range cs.Actions() {
		if a.GetResource().Resource != "secrets" {
			continue
		}
		switch a.GetVerb() {
		case "create", "update", "patch", "delete":
			out = append(out, a.GetVerb()+" "+a.GetNamespace()+"/"+nameOf(a))
		}
	}
	return out
}

func nameOf(a k8stesting.Action) string {
	switch v := a.(type) {
	case k8stesting.UpdateAction:
		return v.GetObject().(*corev1.Secret).Name
	case k8stesting.PatchAction:
		return v.GetName()
	case k8stesting.DeleteAction:
		return v.GetName()
	}
	return ""
}

func runUpdate(t *testing.T, cs *fake.Clientset, args ...string) (string, error) {
	t.Helper()
	d := testDeps(cs, manifest(t, "v0.5.0"), func(io.Reader, io.Writer, ClusterInfo) (bool, error) { return true, nil })
	return execute(t, d, append([]string{"--to", "v0.5.0"}, args...)...)
}

func TestUpdateGeneratesTokenKeysWhenNoneExist(t *testing.T) {
	cs := tokenKeyCluster()
	out, err := runUpdate(t, cs, "--yes")
	if err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	platform := secretData(t, cs, platformSecretNS, "mcp-platform-api-credentials")
	signer, err := passport.NewES256Signer([]byte(platform["JWT_SIGNING_KEY"]))
	if err != nil {
		t.Fatalf("platform-api has no usable JWT_SIGNING_KEY: %v", err)
	}
	token, err := passport.Sign(signer, passport.Principal{Subject: "u", Role: passport.RoleUser}, 60e9, []string{passport.AudienceRuntime})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []struct{ ns, name string }{
		{platformSecretNS, "mcp-platform-api-credentials"},
		{platformSecretNS, "mcp-runtime-api-credentials"},
		{analyticsSecretNS, "mcp-analytics-api-credentials"},
	} {
		data := secretData(t, cs, s.ns, s.name)
		verifier, err := passport.NewES256Verifier([]byte(data["JWT_VERIFY_KEY"]))
		if err != nil {
			t.Fatalf("%s has no usable JWT_VERIFY_KEY: %v", s.name, err)
		}
		if _, err := verifier.Verify(token, passport.AudienceRuntime); err != nil {
			t.Fatalf("%s does not trust the new signing key: %v", s.name, err)
		}
		if data["JWT_SECRET"] != "legacy-shared-secret" || data["API_KEYS"] != "k" {
			t.Fatalf("%s lost existing keys: %v", s.name, data)
		}
		if s.name != "mcp-platform-api-credentials" {
			if _, ok := data["JWT_SIGNING_KEY"]; ok {
				t.Fatalf("%s must never receive the private key", s.name)
			}
		}
	}
	if strings.Contains(out, "PRIVATE KEY") || strings.Contains(out, "PUBLIC KEY") || strings.Contains(out, platform["JWT_SIGNING_KEY"]) {
		t.Fatalf("output leaks key material:\n%s", out)
	}
}

func TestUpdateKeepsExistingTokenKeyPairUntouched(t *testing.T) {
	private, public, err := passport.GenerateES256KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pair := map[string]string{"JWT_VERIFY_KEY": string(public)}
	cs := fake.NewSimpleClientset(append(installedWithoutSecrets(),
		credentialSecret(platformSecretNS, "mcp-platform-api-credentials", map[string]string{"JWT_SIGNING_KEY": string(private), "JWT_VERIFY_KEY": string(public)}),
		credentialSecret(platformSecretNS, "mcp-runtime-api-credentials", pair),
		credentialSecret(analyticsSecretNS, "mcp-analytics-api-credentials", pair))...)
	if out, err := runUpdate(t, cs, "--yes"); err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	if w := secretWrites(cs); len(w) != 0 {
		t.Fatalf("a complete key pair must not be rewritten: %v", w)
	}
}

func TestUpdateExtendsVerifySetAndKeepsOldVerifyKeys(t *testing.T) {
	private, public, _ := passport.GenerateES256KeyPair()
	_, old, _ := passport.GenerateES256KeyPair()
	cs := fake.NewSimpleClientset(append(installedWithoutSecrets(),
		credentialSecret(platformSecretNS, "mcp-platform-api-credentials", map[string]string{"JWT_SIGNING_KEY": string(private), "JWT_VERIFY_KEY": string(old)}),
		credentialSecret(platformSecretNS, "mcp-runtime-api-credentials", nil),
		credentialSecret(analyticsSecretNS, "mcp-analytics-api-credentials", nil))...)
	if out, err := runUpdate(t, cs, "--yes"); err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	platform := secretData(t, cs, platformSecretNS, "mcp-platform-api-credentials")
	if platform["JWT_SIGNING_KEY"] != string(private) {
		t.Fatal("existing signing key was changed")
	}
	for _, s := range []struct{ ns, name string }{{platformSecretNS, "mcp-platform-api-credentials"}, {platformSecretNS, "mcp-runtime-api-credentials"}, {analyticsSecretNS, "mcp-analytics-api-credentials"}} {
		v := secretData(t, cs, s.ns, s.name)["JWT_VERIFY_KEY"]
		if !strings.Contains(v, strings.TrimSpace(string(old))) || !strings.Contains(v, strings.TrimSpace(string(public))) {
			t.Fatalf("%s verify set must hold the old key and the signing key's public key", s.name)
		}
	}
}

func TestUpdateFailsOnUnparsableSigningKeyWithoutChanges(t *testing.T) {
	cs := fake.NewSimpleClientset(append(installedWithoutSecrets(),
		credentialSecret(platformSecretNS, "mcp-platform-api-credentials", map[string]string{"JWT_SIGNING_KEY": "corrupt"}),
		credentialSecret(platformSecretNS, "mcp-runtime-api-credentials", nil),
		credentialSecret(analyticsSecretNS, "mcp-analytics-api-credentials", nil))...)
	_, err := runUpdate(t, cs, "--yes")
	if err == nil || !strings.Contains(err.Error(), "JWT_SIGNING_KEY") {
		t.Fatalf("expected a JWT_SIGNING_KEY error, got %v", err)
	}
	if w := secretWrites(cs); len(w) != 0 {
		t.Fatalf("secrets written: %v", w)
	}
	if p := patchedDeployments(cs); len(p) != 0 {
		t.Fatalf("workloads rolled despite the key error: %v", p)
	}
	if got := secretData(t, cs, platformSecretNS, "mcp-platform-api-credentials")["JWT_SIGNING_KEY"]; got != "corrupt" {
		t.Fatal("unparsable signing key was overwritten")
	}
}

func TestUpdateFailsWhenPlatformCredentialsSecretMissing(t *testing.T) {
	cs := fake.NewSimpleClientset(installedWithoutSecrets()...)
	if _, err := runUpdate(t, cs, "--yes"); err == nil || !strings.Contains(err.Error(), "mcp-platform-api-credentials") {
		t.Fatalf("expected missing owner secret error, got %v", err)
	}
	if p := patchedDeployments(cs); len(p) != 0 {
		t.Fatalf("workloads rolled: %v", p)
	}
}

func TestUpdateDryRunReportsTokenKeyProvisioningWithoutWriting(t *testing.T) {
	cs := tokenKeyCluster()
	out, err := runUpdate(t, cs, "--dry-run")
	if err != nil {
		t.Fatalf("dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Platform token keys") || !strings.Contains(out, "would generate") {
		t.Fatalf("dry-run must report token key provisioning:\n%s", out)
	}
	if strings.Contains(out, "up to date") {
		t.Fatalf("dry-run claimed nothing to do:\n%s", out)
	}
	if w := secretWrites(cs); len(w) != 0 || len(patchedDeployments(cs)) != 0 {
		t.Fatalf("dry-run wrote: %v %v", w, patchedDeployments(cs))
	}
	if _, ok := secretData(t, cs, platformSecretNS, "mcp-platform-api-credentials")["JWT_SIGNING_KEY"]; ok {
		t.Fatal("dry-run generated a key")
	}
	jsonOut, err := runUpdate(t, cs, "--dry-run", "--output", "json")
	if err != nil || !strings.Contains(jsonOut, `"tokenKeys"`) || strings.Contains(jsonOut, "PRIVATE") {
		t.Fatalf("json dry-run: %v\n%s", err, jsonOut)
	}
}

func TestUpdateProvisionsKeysBeforeAnyRolloutAndVerifiersFirst(t *testing.T) {
	cs := tokenKeyCluster()
	if out, err := runUpdate(t, cs, "--yes"); err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	var events []string
	for _, a := range cs.Actions() {
		switch {
		case a.GetResource().Resource == "secrets" && a.GetVerb() != "get" && a.GetVerb() != "list":
			events = append(events, "secret:"+nameOf(a))
		case a.GetResource().Resource == "deployments" && a.GetVerb() == "patch":
			events = append(events, "patch:"+a.(k8stesting.PatchAction).GetName())
		}
	}
	index := func(e string) int {
		for i, v := range events {
			if v == e {
				return i
			}
		}
		t.Fatalf("event %s missing in %v", e, events)
		return -1
	}
	lastSecret := index("secret:mcp-platform-api-credentials")
	for _, e := range []string{"secret:mcp-runtime-api-credentials", "secret:mcp-analytics-api-credentials"} {
		if index(e) > lastSecret {
			t.Fatalf("verifier secrets must be written before the signing key: %v", events)
		}
	}
	for _, e := range events {
		if strings.HasPrefix(e, "patch:") && index(e) < lastSecret {
			t.Fatalf("a workload rolled before the keys were provisioned: %v", events)
		}
	}
	if index("patch:mcp-platform-api") < index("patch:mcp-runtime-api") || index("patch:mcp-platform-api") < index("patch:mcp-analytics-api") {
		t.Fatalf("platform-api must roll after the verifiers: %v", events)
	}
}

func TestUpdateSkipsTokenKeysWhenNoKeyConsumerChanges(t *testing.T) {
	cs := fake.NewSimpleClientset(installedWithoutSecrets()...) // no credential secrets at all
	if out, err := runUpdate(t, cs, "--yes", "--only", "ui"); err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	for _, a := range cs.Actions() {
		if a.GetResource().Resource == "secrets" {
			t.Fatalf("secrets touched for a ui-only update: %v", a)
		}
	}
}

func TestApplyRollsPlatformAPIAfterVerifiers(t *testing.T) {
	cs := fake.NewSimpleClientset(installed("v0.4.0")...)
	plan, err := BuildPlan(context.Background(), cs, manifest(t, "v0.5.0"), Selection{})
	if err != nil {
		t.Fatal(err)
	}
	res := Apply(context.Background(), cs, plan, ApplyOptions{Timeout: 1e9, Waiter: noopWaiter})
	if res.Failed {
		t.Fatalf("%+v", res)
	}
	got := strings.Join(patchedDeployments(cs), ",")
	want := "mcp-runtime-operator-controller-manager,mcp-runtime-api,mcp-analytics-api,mcp-platform-api,mcp-ingest,mcp-processor,mcp-ui"
	if got != want {
		t.Fatalf("rollout order = %s", got)
	}
}
