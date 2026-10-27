package main

import (
	"time"

	policypkg "mcp-runtime/pkg/policy"
)

// policySnapshot is the immutable value swapped through an atomic pointer.
// It separates three facts that used to be independent fields:
//
//   - active: the last validated policy, nil until the first load succeeds.
//     Ready, Revision and LoadedAt are derived from it, so "ready" can never
//     be true without a policy or disagree with the revision it describes.
//   - seed: the default document served (and failed closed on) before any
//     validated policy exists. It is never reported as ready.
//   - reloadErr: diagnostics about the most recent reload. It says nothing
//     about whether the active policy is usable; a failed reload keeps the
//     previous active policy (last-known-good).
//
// Fields are unexported so a snapshot is built only through seedSnapshot,
// readySnapshot and afterFailedReload. Go cannot prevent code in this package
// from assembling a literal or mutating the shared *policypkg.Document; the
// document is treated as read-only by convention once published.
type policySnapshot struct {
	active    *activePolicy
	seed      *policypkg.Document
	reloadErr error
}

// activePolicy is a validated, compiled policy and the facts about its load.
type activePolicy struct {
	doc      *policypkg.Document
	revision string
	loadedAt time.Time
}

// seedSnapshot is the not-ready state served until a policy validates.
func seedSnapshot(doc *policypkg.Document) policySnapshot {
	return policySnapshot{seed: doc}
}

// readySnapshot activates a validated policy loaded at the given instant.
// A nil document cannot be activated: it yields the not-ready zero state.
func readySnapshot(doc *policypkg.Document, loadedAt time.Time) policySnapshot {
	if doc == nil {
		return policySnapshot{}
	}
	return policySnapshot{active: &activePolicy{doc: doc, revision: doc.Revision, loadedAt: loadedAt}}
}

// afterFailedReload records a rejected reload without touching the active
// policy, or the seed when nothing has been activated yet.
func (p policySnapshot) afterFailedReload(err error) policySnapshot {
	p.reloadErr = err
	return p
}

// Ready is true once a validated policy has been activated, and stays true
// across later failed reloads.
func (p policySnapshot) Ready() bool { return p.active != nil }

// Document returns the policy requests are evaluated against: the active
// policy once ready, otherwise the seed (nil only for the zero snapshot).
func (p policySnapshot) Document() *policypkg.Document {
	if p.active != nil {
		return p.active.doc
	}
	return p.seed
}

// Revision is the active policy's revision, empty until ready.
func (p policySnapshot) Revision() string {
	if p.active != nil {
		return p.active.revision
	}
	return ""
}

// LoadedAt is when the active policy was activated, zero until ready.
func (p policySnapshot) LoadedAt() time.Time {
	if p.active != nil {
		return p.active.loadedAt
	}
	return time.Time{}
}

// ReloadErr is the most recent reload failure, for observability only.
func (p policySnapshot) ReloadErr() error { return p.reloadErr }
