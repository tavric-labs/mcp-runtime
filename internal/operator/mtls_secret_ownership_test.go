package operator

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

func TestGeneratedMTLSSecretsFollowServerLifecycle(t *testing.T) {
	for _, gateway := range []bool{true, false} {
		name := "ingress-client"
		if gateway {
			name = "gateway"
		}
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			server := mtlsServer()
			server.UID = types.UID("current-server")
			secretName := traefikClientCertSecretName(server)
			if gateway {
				secretName = gatewayTLSSecretName(server)
			}
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: server.Namespace, UID: types.UID("issued-secret"), Annotations: map[string]string{
				"cert-manager.io/certificate-name": secretName, "cert-manager.io/issuer-kind": "ClusterIssuer", "cert-manager.io/issuer-name": "identity-ca",
			}}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": []byte("original-certificate"), "tls.key": []byte("original-key")}}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(server, secret).WithInterceptorFuncs(interceptor.Funcs{
				Apply: func(context.Context, client.WithWatch, runtime.ApplyConfiguration, ...client.ApplyOption) error {
					return nil
				},
			}).Build()
			r := &MCPServerReconciler{Client: c, Scheme: scheme, AdapterCertificatesEnabled: true, AdapterTrustDomain: "example.org", MTLSClusterIssuer: "identity-ca"}
			reconcile := r.reconcileTraefikClientCertificate
			if gateway {
				reconcile = r.reconcileGatewayCertificate
			}
			for i := 0; i < 2; i++ {
				if err := reconcile(context.Background(), server); err != nil {
					t.Fatal(err)
				}
			}
			var got corev1.Secret
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: server.Namespace, Name: secretName}, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.OwnerReferences) != 1 || got.OwnerReferences[0].UID != server.UID {
				t.Fatalf("generated TLS Secret has no server owner; it would survive retirement: %+v", got.OwnerReferences)
			}
			if got.OwnerReferences[0].Controller != nil && *got.OwnerReferences[0].Controller {
				t.Fatal("must not take certificate controller ownership")
			}
			if string(got.Data["tls.crt"]) != "original-certificate" || string(got.Data["tls.key"]) != "original-key" {
				t.Fatal("ownership repair changed certificate material")
			}
		})
	}
}

func TestIssuedTLSSecretOwnershipPreservesForeignAndUserCredentials(t *testing.T) {
	for _, test := range []struct {
		name      string
		mutate    func(*corev1.Secret)
		wantOwner bool
		wantError bool
	}{
		{"user-provided", func(s *corev1.Secret) { s.Annotations = nil }, false, false},
		{"different-certificate", func(s *corev1.Secret) { s.Annotations["cert-manager.io/certificate-name"] = "other" }, false, false},
		{"different-issuer", func(s *corev1.Secret) { s.Annotations["cert-manager.io/issuer-name"] = "user-issuer" }, false, false},
		{"non-tls", func(s *corev1.Secret) { s.Type = corev1.SecretTypeOpaque }, false, false},
		{"another-server", func(s *corev1.Secret) {
			s.OwnerReferences = []metav1.OwnerReference{{APIVersion: mcpv1alpha1.GroupVersion.String(), Kind: "MCPServer", Name: "other", UID: "other-server"}}
		}, false, true},
		{"cert-manager-controller", func(s *corev1.Secret) {
			yes := true
			s.OwnerReferences = []metav1.OwnerReference{{APIVersion: "cert-manager.io/v1", Kind: "Certificate", Name: s.Name, UID: "certificate-uid", Controller: &yes}}
		}, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			_ = mcpv1alpha1.AddToScheme(scheme)
			server := mtlsServer()
			server.UID = "current-server"
			name := gatewayTLSSecretName(server)
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: server.Namespace, Annotations: map[string]string{"cert-manager.io/certificate-name": name, "cert-manager.io/issuer-kind": "ClusterIssuer", "cert-manager.io/issuer-name": "identity-ca"}}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.key": []byte("unchanged")}}
			test.mutate(secret)
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
			r := &MCPServerReconciler{Client: c, Scheme: scheme, MTLSClusterIssuer: "identity-ca"}
			err := r.reconcileIssuedTLSSecretOwner(context.Background(), server, name)
			if (err != nil) != test.wantError {
				t.Fatalf("error=%v", err)
			}
			if err != nil && !strings.Contains(err.Error(), "another MCPServer") {
				t.Fatal(err)
			}
			var got corev1.Secret
			_ = c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: server.Namespace}, &got)
			owned := false
			for _, o := range got.OwnerReferences {
				if o.UID == server.UID {
					owned = true
				}
			}
			if owned != test.wantOwner {
				t.Fatalf("owner=%v, want=%v", owned, test.wantOwner)
			}
			if string(got.Data["tls.key"]) != "unchanged" {
				t.Fatal("credential material changed")
			}
			if test.name == "cert-manager-controller" && len(got.OwnerReferences) != 2 {
				t.Fatal("cert-manager owner removed")
			}
		})
	}
}
