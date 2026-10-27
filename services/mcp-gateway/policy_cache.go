package main

import (
	"encoding/json"
	"errors"
	"log"
	"math/rand/v2"
	"os"
	"time"

	policypkg "mcp-runtime/pkg/policy"
)

const (
	policyReloadInterval = 5 * time.Second
	policyReloadJitter   = 2 * time.Second
)

func nextPolicyReloadInterval() time.Duration {
	return policyReloadInterval + rand.N(policyReloadJitter)
}

// errPolicyUnavailable is returned by currentPolicy when no validated policy
// snapshot has been activated yet. Callers (including gate filters) fail closed
// in this state.
var errPolicyUnavailable = errors.New("policy_unavailable")

func (s *gatewayServer) startPolicyCache() error {
	// Seed with the default document so reads never observe a nil policy. It is
	// not marked Ready: in file-backed mode the gateway is only ready once the
	// rendered policy validates and activates below.
	s.snapshotPolicy(seedSnapshot(s.defaultPolicyDocument()))
	if err := s.reloadPolicy(); err != nil {
		return err
	}
	if s.policyFile == "" {
		return nil
	}

	go func() {
		ticker := time.NewTicker(nextPolicyReloadInterval())
		defer ticker.Stop()
		for range ticker.C {
			if err := s.reloadPolicy(); err != nil {
				log.Printf("policy reload failed: %v", err)
			}
		}
	}()

	return nil
}

// reloadPolicy loads, validates, and atomically activates the policy. A load or
// validation failure never replaces the last-known-good snapshot: the previous
// active policy, revision, load time and readiness are retained and only the reload error is updated so that
// /config/status and metrics surface the failure while traffic keeps flowing.
func (s *gatewayServer) reloadPolicy() error {
	doc, err := s.loadPolicy()
	if err != nil {
		retained := s.loadPolicySnapshot()
		if retained.Document() == nil {
			retained = seedSnapshot(s.defaultPolicyDocument())
		}
		s.snapshotPolicy(retained.afterFailedReload(err))
		recordPolicyReloadFailure()
		s.metrics.recordPolicyReload(s.metricScope(retained.Document()), err)
		return err
	}

	loadedAt := s.currentTime()
	s.snapshotPolicy(readySnapshot(doc, loadedAt))
	recordPolicyReloadSuccess(doc.Revision, doc.SchemaVersion, loadedAt)
	s.metrics.recordPolicyReload(s.metricScope(doc), nil)
	return nil
}

func (s *gatewayServer) currentPolicy() (*policypkg.Document, error) {
	snapshot := s.loadPolicySnapshot()
	if snapshot.Document() == nil {
		return s.defaultPolicyDocument(), errPolicyUnavailable
	}
	// Until the first valid policy is activated, fail tool calls closed even
	// though a seed document is present. Once Ready, a later failed reload is
	// surfaced via /config/status but traffic keeps using last-known-good.
	if !snapshot.Ready() {
		return snapshot.Document(), errPolicyUnavailable
	}
	return snapshot.Document(), nil
}

func (s *gatewayServer) loadPolicySnapshot() policySnapshot {
	if value := s.policyState.Load(); value != nil {
		return *value
	}
	return seedSnapshot(s.defaultPolicyDocument())
}

func (s *gatewayServer) snapshotPolicy(snapshot policySnapshot) {
	s.policyState.Store(&snapshot)
}

func (s *gatewayServer) loadPolicy() (*policypkg.Document, error) {
	doc := &policypkg.Document{}
	fromFile := false
	if s.policyFile != "" {
		data, err := os.ReadFile(s.policyFile)
		if err != nil {
			return nil, err
		} else if len(data) > 0 {
			if err := json.Unmarshal(data, doc); err != nil {
				return nil, err
			}
			fromFile = true
		}
	}

	// A file-backed document is validated exactly as the operator stamped it:
	// the revision digest covers the rendered content, so integrity must be
	// checked before gateway runtime defaults mutate the document.
	if fromFile {
		if err := policypkg.Validate(doc); err != nil {
			return nil, err
		}
	}

	s.applyPolicyDefaults(doc)

	// A gateway-generated default document (no policy file, or an empty one)
	// is stamped after defaulting so it carries a supported schema version and
	// a deterministic revision over its final content.
	if !fromFile {
		if err := policypkg.Stamp(doc, ""); err != nil {
			return nil, err
		}
		if err := policypkg.Validate(doc); err != nil {
			return nil, err
		}
	}
	return policypkg.Compile(doc)
}

// applyPolicyDefaults fills server identity and auth/policy defaults on a
// decoded document. A missing Auth block remains missing so the gateway fails
// MCP traffic closed until the operator publishes an OAuth policy.
func (s *gatewayServer) applyPolicyDefaults(doc *policypkg.Document) {
	if doc.Policy == nil {
		doc.Policy = &policypkg.Config{}
	}
	if doc.Server.Name == "" {
		doc.Server.Name = policypkg.ServerName(s.serverName)
	}
	if doc.Server.Namespace == "" {
		doc.Server.Namespace = policypkg.Namespace(s.serverNamespace)
	}
	if doc.Server.Cluster == "" {
		doc.Server.Cluster = s.clusterName
	}
	if doc.Auth != nil && !policypkg.UsesDelegatedHeaders(doc) && doc.Auth.TokenHeader == "" {
		doc.Auth.TokenHeader = defaultTokenHeader
	}
	if policypkg.UsesDelegatedHeaders(doc) {
		if doc.Policy.Mode == "" {
			doc.Policy.Mode = "allow-list"
		}
		if doc.Policy.DefaultDecision == "" {
			doc.Policy.DefaultDecision = "deny"
		}
	} else {
		if doc.Policy.Mode == "" {
			doc.Policy.Mode = s.defaultPolicyMode
		}
		if doc.Policy.DefaultDecision == "" {
			doc.Policy.DefaultDecision = s.defaultPolicyDecision
		}
	}
	if doc.Policy.PolicyVersion == "" {
		doc.Policy.PolicyVersion = s.defaultPolicyVersion
	}
}

func policyIdentity(identity identityContext) policypkg.Identity {
	return policypkg.Identity{
		HumanID:   identity.HumanID,
		AgentID:   identity.AgentID,
		TeamID:    identity.TeamID,
		SessionID: identity.SessionID,
	}
}

func (s *gatewayServer) defaultPolicyDocument() *policypkg.Document {
	doc := &policypkg.Document{
		Server: policypkg.Server{
			Name:      policypkg.ServerName(s.serverName),
			Namespace: policypkg.Namespace(s.serverNamespace),
			Cluster:   s.clusterName,
		},
		Policy: &policypkg.Config{
			Mode:            s.defaultPolicyMode,
			DefaultDecision: s.defaultPolicyDecision,
			PolicyVersion:   s.defaultPolicyVersion,
		},
	}
	// Stamp so the default carries a supported schema version and deterministic
	// revision; this is best-effort and the document is well-formed by
	// construction, so any error is non-fatal.
	_ = policypkg.Stamp(doc, "")
	return doc
}
