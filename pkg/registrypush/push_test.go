package registrypush

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

func TestPushDockerArchiveCreatesHelperAndRewritesTarget(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "registry"}})
	tmp, err := os.CreateTemp("", "mcp-registry-push-*.tar")
	if err != nil {
		t.Fatalf("create temp tar: %v", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		t.Fatalf("close temp tar: %v", err)
	}
	defer os.Remove(tmpPath)

	origWait := waitPodReadyHook
	origCopy := copyFileToPodHook
	origExec := execInPodHook
	defer func() {
		waitPodReadyHook = origWait
		copyFileToPodHook = origCopy
		execInPodHook = origExec
	}()

	waitPodReadyHook = func(ctx context.Context, clientset kubernetes.Interface, namespace, name string) error {
		return nil
	}
	copyFileToPodHook = func(ctx context.Context, clientset kubernetes.Interface, cfg *rest.Config, namespace, podName, containerName, srcPath, destPath string) error {
		if srcPath != tmpPath {
			t.Fatalf("srcPath = %q, want %q", srcPath, tmpPath)
		}
		if destPath != defaultImageTarPath {
			t.Fatalf("destPath = %q, want %q", destPath, defaultImageTarPath)
		}
		return nil
	}
	var gotTarget string
	execInPodHook = func(ctx context.Context, clientset kubernetes.Interface, cfg *rest.Config, namespace, podName, containerName string, command []string) error {
		if len(command) == 0 {
			t.Fatal("expected skopeo command")
		}
		gotTarget = command[len(command)-1]
		return nil
	}

	err = PushDockerArchive(context.Background(), client, &rest.Config{Host: "https://example.invalid"}, tmpPath, "registry.example.com/acme/demo:v1", Config{
		HelperNamespace: "registry",
		Hosts: Hosts{
			InternalHostnames: []string{"registry.example.com"},
			ServiceName:       "registry",
			ServiceNamespace:  "registry",
			ServicePort:       5000,
		},
	})
	if err != nil {
		t.Fatalf("PushDockerArchive() error = %v", err)
	}
	if gotTarget != "docker://registry.registry.svc.cluster.local:5000/acme/demo:v1" {
		t.Fatalf("target = %q", gotTarget)
	}
	pods, err := client.CoreV1().Pods("registry").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	if len(pods.Items) != 0 {
		t.Fatalf("expected helper pod cleanup, found %d pod(s)", len(pods.Items))
	}
}

func TestPushDockerArchiveDeletesHelperAfterExecFailure(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "registry"}})
	tmp, err := os.CreateTemp("", "mcp-registry-push-*.tar")
	if err != nil {
		t.Fatalf("create temp tar: %v", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		t.Fatalf("close temp tar: %v", err)
	}
	defer os.Remove(tmpPath)

	origWait := waitPodReadyHook
	origCopy := copyFileToPodHook
	origExec := execInPodHook
	defer func() {
		waitPodReadyHook = origWait
		copyFileToPodHook = origCopy
		execInPodHook = origExec
	}()

	waitPodReadyHook = func(ctx context.Context, clientset kubernetes.Interface, namespace, name string) error { return nil }
	copyFileToPodHook = func(ctx context.Context, clientset kubernetes.Interface, cfg *rest.Config, namespace, podName, containerName, srcPath, destPath string) error {
		return nil
	}
	execInPodHook = func(ctx context.Context, clientset kubernetes.Interface, cfg *rest.Config, namespace, podName, containerName string, command []string) error {
		return os.ErrPermission
	}

	err = PushDockerArchive(context.Background(), client, &rest.Config{Host: "https://example.invalid"}, tmpPath, "registry.example.com/acme/demo:v1", Config{
		HelperNamespace: "registry",
	})
	if err == nil || !strings.Contains(err.Error(), "push image from helper pod") {
		t.Fatalf("PushDockerArchive() error = %v, want wrapped exec failure", err)
	}
	pods, err := client.CoreV1().Pods("registry").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	if len(pods.Items) != 0 {
		t.Fatalf("expected helper pod cleanup after failure, found %d pod(s)", len(pods.Items))
	}
}

func TestPushDockerArchiveTarFetchSkipsExecCopy(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mcp-platform"}})
	tmp, err := os.CreateTemp("", "mcp-registry-push-*.tar")
	if err != nil {
		t.Fatalf("create temp tar: %v", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		t.Fatalf("close temp tar: %v", err)
	}
	defer os.Remove(tmpPath)

	origWaitReady := waitPodReadyHook
	origWaitSucceeded := waitPodSucceededHook
	origCopy := copyFileToPodHook
	origExec := execInPodHook
	defer func() {
		waitPodReadyHook = origWaitReady
		waitPodSucceededHook = origWaitSucceeded
		copyFileToPodHook = origCopy
		execInPodHook = origExec
	}()

	copyCalled := false
	copyFileToPodHook = func(ctx context.Context, clientset kubernetes.Interface, cfg *rest.Config, namespace, podName, containerName, srcPath, destPath string) error {
		copyCalled = true
		return nil
	}
	execCalled := false
	execInPodHook = func(ctx context.Context, clientset kubernetes.Interface, cfg *rest.Config, namespace, podName, containerName string, command []string) error {
		execCalled = true
		return nil
	}
	waitPodSucceededHook = func(ctx context.Context, clientset kubernetes.Interface, namespace, name string) error {
		return nil
	}

	err = PushDockerArchive(context.Background(), client, &rest.Config{Host: "https://example.invalid"}, tmpPath, "registry.example.com/acme/demo:v1", Config{
		HelperNamespace: "mcp-platform",
		TarFetchURL:     "http://10.0.0.5:8080/internal/registry-push/tar",
		TarFetchToken:   "test",
		Hosts: Hosts{
			InternalHostnames: []string{"registry.example.com"},
			ServiceName:       "registry",
			ServiceNamespace:  "registry",
			ServicePort:       5000,
		},
	})
	if err != nil {
		t.Fatalf("PushDockerArchive() error = %v", err)
	}
	if copyCalled {
		t.Fatal("copyFileToPod should not run when TarFetchURL is set")
	}
	if execCalled {
		t.Fatal("execInPod should not run when TarFetchURL is set")
	}
	pod, err := client.CoreV1().Pods("mcp-platform").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	if len(pod.Items) != 0 {
		t.Fatalf("expected helper pod cleanup, found %d pod(s)", len(pod.Items))
	}
}

func TestPushDockerArchiveTarFetchFailureIncludesHelperDiagnostics(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "mcp-platform"}})
	origWait := waitPodSucceededHook
	defer func() { waitPodSucceededHook = origWait }()
	waitPodSucceededHook = func(ctx context.Context, clientset kubernetes.Interface, namespace, name string) error {
		pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get helper pod: %v", err)
		}
		pod.Status.Phase = corev1.PodFailed
		pod.Status.Reason = "RegistryUnavailable"
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name: name,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 6,
				Reason:   "Error",
				Message:  "registry DNS lookup failed",
			}},
		}}
		if _, err := clientset.CoreV1().Pods(namespace).UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("update helper pod status: %v", err)
		}
		return os.ErrPermission
	}

	err := PushDockerArchive(context.Background(), client, &rest.Config{Host: "https://example.invalid"}, "/tmp/image.tar", "registry.example.com/acme/demo:v1", Config{
		HelperNamespace: "mcp-platform",
		TarFetchURL:     "http://10.0.0.5:8080/internal/registry-push/tar",
	})
	if err == nil || !strings.Contains(err.Error(), "helper diagnostics: phase=Failed") || !strings.Contains(err.Error(), "registry DNS lookup failed") {
		t.Fatalf("PushDockerArchive() error = %v, want helper pod failure details", err)
	}
	pods, err := client.CoreV1().Pods("mcp-platform").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	if len(pods.Items) != 0 {
		t.Fatalf("expected helper pod cleanup after failure, found %d pod(s)", len(pods.Items))
	}
}

func TestNewHelperNameIncludesRandomSuffix(t *testing.T) {
	first := newHelperName()
	second := newHelperName()
	if !strings.HasPrefix(first, "registry-pusher-") || !strings.HasPrefix(second, "registry-pusher-") {
		t.Fatalf("unexpected helper names %q %q", first, second)
	}
	if first == second {
		t.Fatalf("expected unique helper names, got %q", first)
	}
}

func TestHelperCommandRetriesSkopeoWithoutRefetchingOneTimeTar(t *testing.T) {
	binDir := t.TempDir()
	workDir := t.TempDir()
	curlCountFile := workDir + "/curl.count"
	skopeoCountFile := workDir + "/skopeo.count"
	tarOut := workDir + "/image.tar"

	writeExecutable := func(name, body string) {
		t.Helper()
		path := binDir + "/" + name
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	writeExecutable("curl", "#!/bin/sh\nset -eu\ncount=0\nif [ -f \""+curlCountFile+"\" ]; then count=$(cat \""+curlCountFile+"\"); fi\ncount=$((count + 1))\necho \"$count\" > \""+curlCountFile+"\"\nif [ \"$count\" -gt 1 ]; then\n  echo \"one-time tar URL fetched more than once\" >&2\n  exit 22\nfi\necho \"archive\" > \""+tarOut+"\"\n")
	writeExecutable("skopeo", "#!/bin/sh\nset -eu\ncount=0\nif [ -f \""+skopeoCountFile+"\" ]; then count=$(cat \""+skopeoCountFile+"\"); fi\ncount=$((count + 1))\necho \"$count\" > \""+skopeoCountFile+"\"\nif [ \"$count\" -lt 3 ]; then\n  echo \"pinging container registry registry.registry.svc.cluster.local:5000: dial tcp 10.43.209.245:5000: connect: connection refused\" >&2\n  exit 1\nfi\n")
	writeExecutable("sleep", "#!/bin/sh\nexit 0\n")

	cmd := helperCommand(Config{
		AuthSecretName: "mcp-registry-publisher",
		TarFetchURL:    "http://10.42.0.78:8084/internal/registry-push/tar",
		TarFetchToken:  "one-time-token",
	}, "registry.registry.svc.cluster.local:5000/acme/tools:v0.1.0")
	if len(cmd) != 3 || cmd[0] != "sh" || cmd[1] != "-c" {
		t.Fatalf("unexpected helperCommand: %v", cmd)
	}
	script := strings.ReplaceAll(cmd[2], defaultImageTarPath, tarOut)

	execCmd := exec.Command("sh", "-c", script)
	execCmd.Env = []string{"PATH=" + binDir + ":/usr/bin:/bin"}
	out, err := execCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper script failed: %v\n%s", err, string(out))
	}
	curlCount, err := os.ReadFile(curlCountFile)
	if err != nil || strings.TrimSpace(string(curlCount)) != "1" {
		t.Fatalf("curl count = %q, %v; want 1", string(curlCount), err)
	}
	skopeoCount, err := os.ReadFile(skopeoCountFile)
	if err != nil || strings.TrimSpace(string(skopeoCount)) != "3" {
		t.Fatalf("skopeo count = %q, %v; want 3", string(skopeoCount), err)
	}
}

func TestNativePublicationMountsCredentialFile(t *testing.T) {
	client := fake.NewSimpleClientset(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "registry", Namespace: "registry"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "registry", Env: []corev1.EnvVar{{Name: "REGISTRY_AUTH", Value: "token"}}}}}}}})
	old := waitPodSucceededHook
	defer func() { waitPodSucceededHook = old }()
	waitPodSucceededHook = func(ctx context.Context, client kubernetes.Interface, namespace, name string) error {
		pod, err := client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		found := false
		for _, volume := range pod.Spec.Volumes {
			if volume.Secret != nil && volume.Secret.SecretName == "mcp-registry-publisher" {
				found = true
			}
		}
		if !found {
			t.Fatal("missing publisher credential volume")
		}
		if pod.Labels[HelperLabelKey] != HelperLabelValue {
			t.Fatal("helper pod must carry the registry NetworkPolicy label")
		}
		if !strings.Contains(strings.Join(pod.Spec.Containers[0].Command, " "), "--dest-authfile=/registry-publisher/config.json") {
			t.Fatal("missing credential file argument")
		}
		return nil
	}
	cfg := Config{TarFetchURL: "http://runtime-api/image.tar"}
	if err := PushDockerArchive(context.Background(), client, &rest.Config{}, "fixture.tar", "registry.registry.svc:5000/acme/app:test", cfg); err != nil {
		t.Fatal(err)
	}
	cfg.HelperNamespace = "mcp-team-acme"
	if err := PushDockerArchive(context.Background(), client, &rest.Config{}, "fixture.tar", "registry.registry.svc:5000/acme/app:test", cfg); err == nil {
		t.Fatal("publisher allowed outside trusted namespace")
	}
}

func TestResolveHelperNamespaceUsesLiveRegistryAuthentication(t *testing.T) {
	for _, tc := range []struct{ name, auth, preferred, want string }{
		{"native overrides platform", "token", "mcp-platform", "registry"},
		{"native overrides configured namespace", "token", "other", "registry"},
		{"anonymous keeps platform", "", "mcp-platform", "mcp-platform"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewSimpleClientset(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "registry", Namespace: "registry"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "registry", Env: []corev1.EnvVar{{Name: "REGISTRY_AUTH", Value: tc.auth}}}}}}}})
			got, err := ResolveHelperNamespace(context.Background(), client, tc.preferred)
			if err != nil || got != tc.want {
				t.Fatalf("namespace = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestResolveHelperNamespaceFailsClosedOnRegistryReadError(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("get", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("registry read denied")
	})
	if namespace, err := ResolveHelperNamespace(context.Background(), client, "mcp-platform"); err == nil || namespace != "" {
		t.Fatalf("read failure must not select an unauthenticated helper namespace: %q, %v", namespace, err)
	}
}
