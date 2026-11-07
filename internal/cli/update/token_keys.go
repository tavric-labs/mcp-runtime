package update

import (
	"bytes"
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/passport"
	"mcp-runtime/pkg/topology"
)

const (
	signingKeyName = passport.EnvSigningKey
	verifyKeyName  = passport.EnvVerifyKeys
)

// Token key actions reported in the plan.
const (
	tokenKeysGenerate = "generate"
	tokenKeysExtend   = "extend"
	tokenKeysSync     = "sync"
	tokenKeysKeep     = "keep"
)

// TokenKeyPlan summarizes platform token key provisioning. It never carries key
// material.
type TokenKeyPlan struct {
	// Action is generate (no signing key yet), extend (add the signing key's
	// public key to the verification set), sync (copy the verification set to
	// verifier Secrets that lack it) or keep (nothing to write).
	Action string `json:"action"`
	// Secrets lists the Secrets (namespace/name) that would be written.
	Secrets []string `json:"secrets,omitempty"`

	writes []secretWrite
}

// Needed reports whether provisioning writes anything.
func (p *TokenKeyPlan) Needed() bool { return p != nil && len(p.writes) > 0 }

type secretWrite struct {
	namespace, name string
	data            map[string][]byte
}

// tokenKeyComponents are the components whose rollout needs the token key pair.
var tokenKeyComponents = map[string]bool{"platform-api": true, "runtime-api": true, "analytics-api": true}

func needsTokenKeys(plan *Plan) bool {
	for _, r := range plan.Changed() {
		if tokenKeyComponents[r.Component] {
			return true
		}
	}
	return false
}

// credentialSecrets returns the Secrets that carry key from topology, owner first.
func credentialSecrets(key string) []topology.CredentialSet {
	var out []topology.CredentialSet
	for _, set := range topology.CredentialSets() {
		for _, k := range set.Keys {
			if k == key {
				out = append(out, set)
				break
			}
		}
	}
	return out
}

// planTokenKeys reads the live credential Secrets and decides what is needed
// for the ES256 platform token key pair. It never writes. An existing signing
// key and verification set are kept, a missing signing key is generated, and the
// signing key's public key is added to the verification set. A signing key or
// verification set that cannot be parsed is an error and is never overwritten.
func planTokenKeys(ctx context.Context, cs kubernetes.Interface) (*TokenKeyPlan, error) {
	signers := credentialSecrets(signingKeyName)
	verifiers := credentialSecrets(verifyKeyName)
	if len(signers) != 1 || len(verifiers) == 0 || verifiers[0].Name != signers[0].Name {
		return nil, fmt.Errorf("credential topology does not define a single %s owner", signingKeyName)
	}
	owner := signers[0]
	ownerSecret, err := cs.CoreV1().Secrets(owner.Namespace()).Get(ctx, owner.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read secret %s/%s: %w; run `mcp-runtime setup` first (update never creates credentials)", owner.Namespace(), owner.Name, err)
	}
	existingSigning := ownerSecret.Data[signingKeyName]
	existingVerify := ownerSecret.Data[verifyKeyName]
	signing, verify, err := passport.EnsureKeyPair(existingSigning, existingVerify)
	if err != nil {
		return nil, fmt.Errorf("%s/%s %s or %s is not usable (%v); update did not change it", owner.Namespace(), owner.Name, signingKeyName, verifyKeyName, err)
	}

	plan := &TokenKeyPlan{Action: tokenKeysKeep}
	switch {
	case len(bytes.TrimSpace(existingSigning)) == 0:
		plan.Action = tokenKeysGenerate
	case !bytes.Equal(verify, existingVerify):
		plan.Action = tokenKeysExtend
	}
	// Verifier Secrets first, so every verifier trusts the key before the
	// signing key reaches platform-api.
	for _, set := range verifiers[1:] {
		secret, err := cs.CoreV1().Secrets(set.Namespace()).Get(ctx, set.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue // optional component (for example analytics) is not installed
		}
		if err != nil {
			return nil, fmt.Errorf("read secret %s/%s: %w", set.Namespace(), set.Name, err)
		}
		if bytes.Equal(secret.Data[verifyKeyName], verify) {
			continue
		}
		plan.writes = append(plan.writes, secretWrite{set.Namespace(), set.Name, map[string][]byte{verifyKeyName: verify}})
		if plan.Action == tokenKeysKeep {
			plan.Action = tokenKeysSync
		}
	}
	ownerData := map[string][]byte{}
	if !bytes.Equal(existingSigning, signing) {
		ownerData[signingKeyName] = signing
	}
	if !bytes.Equal(existingVerify, verify) {
		ownerData[verifyKeyName] = verify
	}
	if len(ownerData) > 0 {
		plan.writes = append(plan.writes, secretWrite{owner.Namespace(), owner.Name, ownerData})
	}
	for _, w := range plan.writes {
		plan.Secrets = append(plan.Secrets, w.namespace+"/"+w.name)
	}
	return plan, nil
}

// provisionTokenKeys writes the planned keys. Only JWT_SIGNING_KEY and
// JWT_VERIFY_KEY are set; every other key (including the legacy JWT_SECRET) is
// left as is. Writes are additive, so a failed or rolled-back update leaves
// Secrets an earlier release still works with.
func provisionTokenKeys(ctx context.Context, cs kubernetes.Interface, plan *TokenKeyPlan) error {
	for _, w := range plan.writes {
		secret, err := cs.CoreV1().Secrets(w.namespace).Get(ctx, w.name, metav1.GetOptions{})
		if err != nil {
			return core.WrapWithBase(core.ErrUpdateInventoryFailed, err, fmt.Sprintf("read secret %s/%s: %v", w.namespace, w.name, err))
		}
		next := secret.DeepCopy()
		setSecretData(next, w.data)
		if _, err := cs.CoreV1().Secrets(w.namespace).Update(ctx, next, metav1.UpdateOptions{FieldManager: "mcp-runtime-update"}); err != nil {
			return core.WrapWithBase(core.ErrUpdateInventoryFailed, err, fmt.Sprintf("write token keys to secret %s/%s: %v", w.namespace, w.name, err))
		}
	}
	return nil
}

func setSecretData(s *corev1.Secret, data map[string][]byte) {
	if s.Data == nil {
		s.Data = map[string][]byte{}
	}
	for k, v := range data {
		s.Data[k] = v
	}
}
