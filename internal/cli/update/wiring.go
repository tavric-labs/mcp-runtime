package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"mcp-runtime/internal/platformrelease"
)

// configHashAnnotationPrefix marks the pod-template annotation that records
// which version of a declared ConfigMap a workload started with. Changing it
// rolls the workload, which is how config-only changes reach pods that read the
// ConfigMap at start.
const configHashAnnotationPrefix = "mcpruntime.org/config-hash."

// wiringChange is the planned non-image Deployment change for a component,
// plus the values to restore on rollback.
type wiringChange struct {
	resourceHistoryToken *resourceHistoryTokenChange
	serviceAccount       string // target; "" means unchanged
	prevSA               string

	automount     *bool // target; nil means unchanged
	setAutomount  bool
	prevAutomount *bool

	env     map[string]string  // target literal values
	prevEnv map[string]*string // previous literal value; nil entry = was absent
	// prevEnvFrom lists vars that were Secret/ConfigMap references; rollback
	// leaves them as the literal rather than guessing the reference.
	prevEnvFrom map[string]bool

	// secretEnv are env vars to source from a Secret key, with the definition
	// each replaced (nil when it was absent) so rollback can restore it.
	secretEnv []secretEnvChange
	// removeEnv are retired env vars with their previous definition.
	removeEnv []removedEnv

	// args is the full desired container argument list (the field is atomic).
	args     []string
	prevArgs []string
	setArgs  bool

	// dns is the full desired resolver option list (the field is atomic).
	dns       []corev1.PodDNSConfigOption
	prevDNS   []corev1.PodDNSConfigOption
	prevDNSNo bool // the pod had no dnsConfig at all
	setDNS    bool

	// configHash maps pod-template annotation keys to the wanted hash;
	// prevHash holds the previous value, nil meaning the annotation was absent.
	configHash map[string]string
	prevHash   map[string]*string
}

type secretEnvChange struct {
	ref  platformrelease.SecretEnvRef
	prev *corev1.EnvVar
}

type removedEnv struct {
	name string
	prev corev1.EnvVar
}

// secretRefMatches reports whether e is exactly the wanted secretKeyRef.
func secretRefMatches(e *corev1.EnvVar, ref platformrelease.SecretEnvRef) bool {
	if e == nil || e.Value != "" || e.ValueFrom == nil {
		return false
	}
	src := e.ValueFrom
	if src.SecretKeyRef == nil || src.ConfigMapKeyRef != nil || src.FieldRef != nil || src.ResourceFieldRef != nil {
		return false
	}
	k := src.SecretKeyRef
	return k.Name == ref.Secret && k.Key == ref.Key && (k.Optional != nil && *k.Optional) == ref.Optional
}

func findEnv(env []corev1.EnvVar, name string) *corev1.EnvVar {
	for i := range env {
		if env[i].Name == name {
			return &env[i]
		}
	}
	return nil
}

// envVarSourceKeys are every valueFrom source; a patch nulls the ones it does
// not set so a merge never leaves a stale source beside the new one.
var envVarSourceKeys = []string{"secretKeyRef", "configMapKeyRef", "fieldRef", "resourceFieldRef"}

// envVarPatch renders env as a strategic-merge entry that fully replaces any
// previous definition of the same name (literal or reference).
func envVarPatch(e corev1.EnvVar) map[string]any {
	entry := map[string]any{"name": e.Name}
	if e.ValueFrom == nil {
		entry["value"] = e.Value
		entry["valueFrom"] = nil
		return entry
	}
	entry["value"] = nil
	source := map[string]any{}
	if raw, err := json.Marshal(e.ValueFrom); err == nil {
		_ = json.Unmarshal(raw, &source)
	}
	for _, k := range envVarSourceKeys {
		if _, ok := source[k]; !ok {
			source[k] = nil
		}
	}
	if ref, ok := source["secretKeyRef"].(map[string]any); ok {
		// merge would keep a stale optional flag when it is omitted
		if _, has := ref["optional"]; !has {
			ref["optional"] = false
		}
	}
	entry["valueFrom"] = source
	return entry
}

func secretEnvVar(ref platformrelease.SecretEnvRef) corev1.EnvVar {
	optional := ref.Optional
	return corev1.EnvVar{Name: ref.Name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: ref.Secret}, Key: ref.Key, Optional: &optional}}}
}

// configHashes computes the annotation set a component needs for the ConfigMaps
// that must restart it, keyed by annotation name.
func configHashes(component string, cms []platformrelease.ManifestConfigMap) map[string]string {
	out := map[string]string{}
	for _, cm := range cms {
		for _, dependent := range cm.Restart {
			if dependent == component {
				out[configHashAnnotationPrefix+cm.Name] = hashConfigData(cm.Data)
			}
		}
	}
	return out
}

func hashConfigData(data map[string]string) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%d:%s=%d:%s;", len(k), k, len(data[k]), data[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ensureArgs returns the argument list with the wanted flags applied: a flag
// that is already present verbatim is left alone, a repeatable flag is appended
// when its exact value is missing, and any other flag replaces an existing
// occurrence of the same name or is appended.
func ensureArgs(current, want []string) ([]string, bool) {
	out := append([]string(nil), current...)
	changed := false
	for _, arg := range want {
		present := false
		for _, existing := range out {
			if existing == arg {
				present = true
				break
			}
		}
		if present {
			continue
		}
		changed = true
		name := platformrelease.FlagName(arg)
		if platformrelease.RepeatableFlags[name] {
			out = append(out, arg)
			continue
		}
		replaced := false
		for i, existing := range out {
			if platformrelease.FlagName(existing) == name {
				out[i] = arg
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, arg)
		}
	}
	return out, changed
}

// ensureDNSOptions returns the option list with the wanted options applied.
func ensureDNSOptions(current []corev1.PodDNSConfigOption, want []platformrelease.DNSOption) ([]corev1.PodDNSConfigOption, bool) {
	out := append([]corev1.PodDNSConfigOption(nil), current...)
	changed := false
	for _, w := range want {
		value := w.Value
		found := false
		for i := range out {
			if out[i].Name != w.Name {
				continue
			}
			found = true
			existing := ""
			if out[i].Value != nil {
				existing = *out[i].Value
			}
			if existing != value {
				v := value
				out[i].Value = &v
				changed = true
			}
		}
		if !found {
			opt := corev1.PodDNSConfigOption{Name: w.Name}
			if value != "" {
				v := value
				opt.Value = &v
			}
			out = append(out, opt)
			changed = true
		}
	}
	return out, changed
}

// planWiring compares a release's wiring and config hashes with the live
// Deployment and returns the drift to correct (nil when it already matches).
func planWiring(deploy *appsv1.Deployment, container *corev1.Container, want *platformrelease.ComponentWiring, hashes map[string]string) (*wiringChange, []string) {
	if want.IsZero() && len(hashes) == 0 {
		return nil, nil
	}
	change := &wiringChange{prevEnv: map[string]*string{}, prevEnvFrom: map[string]bool{}, env: map[string]string{}}
	var drift []string
	spec := deploy.Spec.Template.Spec

	if want != nil {
		if want.ResourceHistoryToken {
			change.resourceHistoryToken = planResourceHistoryToken(spec, container)
			if change.resourceHistoryToken != nil {
				drift = append(drift, "fixed Prometheus resource-history projected token corrected")
			}
		}
		if want.ServiceAccountName != "" && spec.ServiceAccountName != want.ServiceAccountName {
			change.serviceAccount = want.ServiceAccountName
			change.prevSA = spec.ServiceAccountName
			drift = append(drift, fmt.Sprintf("serviceAccountName %q -> %q", spec.ServiceAccountName, want.ServiceAccountName))
		}
		if want.AutomountServiceAccountToken != nil {
			live := spec.AutomountServiceAccountToken
			if live == nil || *live != *want.AutomountServiceAccountToken {
				change.setAutomount = true
				change.automount = want.AutomountServiceAccountToken
				change.prevAutomount = live
				drift = append(drift, fmt.Sprintf("automountServiceAccountToken -> %t", *want.AutomountServiceAccountToken))
			}
		}
		names := make([]string, 0, len(want.Env))
		for name := range want.Env {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			target := want.Env[name]
			var existing *corev1.EnvVar
			for i := range container.Env {
				if container.Env[i].Name == name {
					existing = &container.Env[i]
				}
			}
			switch {
			case existing == nil:
				change.env[name] = target
				change.prevEnv[name] = nil
				drift = append(drift, fmt.Sprintf("env %s added", name))
			case existing.ValueFrom != nil || existing.Value != target:
				change.env[name] = target
				prev := existing.Value
				change.prevEnv[name] = &prev
				if existing.ValueFrom != nil {
					change.prevEnvFrom[name] = true
				}
				drift = append(drift, fmt.Sprintf("env %s corrected", name))
			}
		}
		for _, ref := range want.SecretEnv {
			existing := findEnv(container.Env, ref.Name)
			if secretRefMatches(existing, ref) {
				continue
			}
			sc := secretEnvChange{ref: ref}
			verb := "set from secret"
			if existing != nil {
				prev := *existing.DeepCopy()
				sc.prev = &prev
				verb = "corrected to secret"
			}
			change.secretEnv = append(change.secretEnv, sc)
			drift = append(drift, fmt.Sprintf("env %s %s %s key %s", ref.Name, verb, ref.Secret, ref.Key))
		}
		for _, name := range want.RemoveEnv {
			if existing := findEnv(container.Env, name); existing != nil {
				change.removeEnv = append(change.removeEnv, removedEnv{name: name, prev: *existing.DeepCopy()})
				drift = append(drift, fmt.Sprintf("env %s removed", name))
			}
		}
		if len(want.Args) > 0 {
			if args, changed := ensureArgs(container.Args, want.Args); changed {
				change.setArgs = true
				change.args = args
				change.prevArgs = append([]string(nil), container.Args...)
				drift = append(drift, "container args: "+strings.Join(missingArgs(container.Args, want.Args), " "))
			}
		}
		if len(want.DNSOptions) > 0 {
			var current []corev1.PodDNSConfigOption
			if spec.DNSConfig != nil {
				current = spec.DNSConfig.Options
			}
			if options, changed := ensureDNSOptions(current, want.DNSOptions); changed {
				change.setDNS = true
				change.dns = options
				change.prevDNS = append([]corev1.PodDNSConfigOption(nil), current...)
				change.prevDNSNo = spec.DNSConfig == nil
				var names []string
				for _, o := range want.DNSOptions {
					names = append(names, o.Name)
				}
				drift = append(drift, "dnsConfig options: "+strings.Join(names, ", "))
			}
		}
	}
	if len(hashes) > 0 {
		keys := make([]string, 0, len(hashes))
		for k := range hashes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			live, has := deploy.Spec.Template.Annotations[key]
			if has && live == hashes[key] {
				continue
			}
			if change.configHash == nil {
				change.configHash = map[string]string{}
				change.prevHash = map[string]*string{}
			}
			change.configHash[key] = hashes[key]
			if has {
				prev := live
				change.prevHash[key] = &prev
			} else {
				change.prevHash[key] = nil
			}
			drift = append(drift, "config "+strings.TrimPrefix(key, configHashAnnotationPrefix)+" changed; workload restarts")
		}
	}
	if len(drift) == 0 {
		return nil, nil
	}
	return change, drift
}

func missingArgs(current, want []string) []string {
	var out []string
	for _, w := range want {
		found := false
		for _, c := range current {
			if c == w {
				found = true
			}
		}
		if !found {
			out = append(out, w)
		}
	}
	return out
}

// wiringSatisfied reports whether the live Deployment already matches change.
func wiringSatisfied(deploy *appsv1.Deployment, container *corev1.Container, change *wiringChange) bool {
	if change == nil {
		return true
	}
	spec := deploy.Spec.Template.Spec
	if change.resourceHistoryToken != nil && !resourceHistoryTokenSatisfied(spec, container) {
		return false
	}
	if change.serviceAccount != "" && spec.ServiceAccountName != change.serviceAccount {
		return false
	}
	if change.setAutomount && (spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken != *change.automount) {
		return false
	}
	for name, target := range change.env {
		if envValue(container.Env, name) != target {
			return false
		}
		for _, e := range container.Env {
			if e.Name == name && e.ValueFrom != nil {
				return false
			}
		}
	}
	for _, sc := range change.secretEnv {
		if !secretRefMatches(findEnv(container.Env, sc.ref.Name), sc.ref) {
			return false
		}
	}
	for _, r := range change.removeEnv {
		if findEnv(container.Env, r.name) != nil {
			return false
		}
	}
	if change.setArgs && !reflect.DeepEqual(container.Args, change.args) {
		return false
	}
	if change.setDNS {
		var current []corev1.PodDNSConfigOption
		if spec.DNSConfig != nil {
			current = spec.DNSConfig.Options
		}
		if !reflect.DeepEqual(current, change.dns) {
			return false
		}
	}
	for key, want := range change.configHash {
		if deploy.Spec.Template.Annotations[key] != want {
			return false
		}
	}
	return true
}

// patchParts collects what a wiring change adds to a Deployment patch.
type patchParts struct {
	podSpec     map[string]any
	annotations map[string]any
}

// apply adds the wiring (or, when rollback is true, its previous values) to the
// pod-spec and pod-template-annotation portions of a strategic-merge patch and
// returns the container entries (env, args) to merge into the container.
func (c *wiringChange) apply(parts *patchParts, rollback bool) (env []map[string]any, args []string, setArgs bool) {
	if c == nil {
		return nil, nil, false
	}
	podSpec := parts.podSpec
	if c.resourceHistoryToken != nil {
		podSpec["volumes"] = []map[string]any{c.resourceHistoryToken.volumePatch(rollback)}
	}
	if c.serviceAccount != "" {
		if rollback {
			podSpec["serviceAccountName"] = c.prevSA
		} else {
			podSpec["serviceAccountName"] = c.serviceAccount
		}
	}
	if c.setAutomount {
		switch {
		case rollback && c.prevAutomount == nil:
			podSpec["automountServiceAccountToken"] = nil // null removes the field
		case rollback:
			podSpec["automountServiceAccountToken"] = *c.prevAutomount
		default:
			podSpec["automountServiceAccountToken"] = *c.automount
		}
	}
	if c.setDNS {
		switch {
		case rollback && c.prevDNSNo:
			podSpec["dnsConfig"] = nil
		case rollback:
			podSpec["dnsConfig"] = map[string]any{"options": dnsOptionsJSON(c.prevDNS)}
		default:
			podSpec["dnsConfig"] = map[string]any{"options": dnsOptionsJSON(c.dns)}
		}
	}
	for key, want := range c.configHash {
		switch {
		case !rollback:
			parts.annotations[key] = want
		case c.prevHash[key] == nil:
			parts.annotations[key] = nil
		default:
			parts.annotations[key] = *c.prevHash[key]
		}
	}
	if c.setArgs {
		setArgs = true
		if rollback {
			args = c.prevArgs
		} else {
			args = c.args
		}
	}
	names := make([]string, 0, len(c.env))
	for name := range c.env {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		switch {
		case !rollback:
			env = append(env, map[string]any{"name": name, "value": c.env[name], "valueFrom": nil})
		case c.prevEnvFrom[name]:
			// Previous value was a reference; keep the literal rather than guess it.
		case c.prevEnv[name] == nil:
			env = append(env, map[string]any{"name": name, "$patch": "delete"})
		default:
			env = append(env, map[string]any{"name": name, "value": *c.prevEnv[name], "valueFrom": nil})
		}
	}
	for _, sc := range c.secretEnv {
		switch {
		case !rollback:
			env = append(env, envVarPatch(secretEnvVar(sc.ref)))
		case sc.prev == nil:
			env = append(env, map[string]any{"name": sc.ref.Name, "$patch": "delete"})
		default:
			env = append(env, envVarPatch(*sc.prev))
		}
	}
	for _, r := range c.removeEnv {
		if rollback {
			env = append(env, envVarPatch(r.prev))
		} else {
			env = append(env, map[string]any{"name": r.name, "$patch": "delete"})
		}
	}
	return env, args, setArgs
}

func dnsOptionsJSON(options []corev1.PodDNSConfigOption) []map[string]any {
	out := make([]map[string]any, 0, len(options))
	for _, o := range options {
		item := map[string]any{"name": o.Name}
		if o.Value != nil {
			item["value"] = *o.Value
		}
		out = append(out, item)
	}
	return out
}

func hasWiring(plan *Plan) bool {
	for _, r := range plan.Rows {
		if r.wiring != nil {
			return true
		}
	}
	return false
}

// recoveryCommands are the kubectl commands that undo the secret env wiring by
// hand: unset what update added and restore a retired env that was a Secret
// reference. A retired literal is never printed (it may be a credential);
// `rollout undo` restores it.
func (c *wiringChange) recoveryCommands(namespace, deployment, container string) []string {
	if c == nil {
		return nil
	}
	var cmds []string
	base := fmt.Sprintf("kubectl -n %s set env deployment/%s -c %s", namespace, deployment, container)
	for _, sc := range c.secretEnv {
		if sc.prev == nil {
			cmds = append(cmds, fmt.Sprintf("%s %s-", base, sc.ref.Name))
		}
	}
	for _, r := range c.removeEnv {
		if ref := r.prev.ValueFrom; ref != nil && ref.SecretKeyRef != nil && ref.SecretKeyRef.Key == r.name {
			cmds = append(cmds, fmt.Sprintf("%s --from=secret/%s --keys=%s", base, ref.SecretKeyRef.Name, r.name))
		}
	}
	return cmds
}
