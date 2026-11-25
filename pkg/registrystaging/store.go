// Package registrystaging implements durable, atomic staging for OCI/Docker
// archive uploads prior to queued registry publication.
//
// Artifacts are never stored in Postgres rows or unconfigured ephemeral /tmp
// directories. Partial uploads remain isolated under an incoming staging prefix
// until Finalize validates size and SHA-256 checksum, fsyncs the artifact, and
// atomically renames it into an immutable finalized path.
package registrystaging

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	// ErrInvalidConfig indicates an invalid staging store configuration.
	ErrInvalidConfig = errors.New("invalid staging store configuration")
	// ErrInvalidReference indicates a malformed or unsafe upload/artifact reference.
	ErrInvalidReference = errors.New("invalid staging reference")
	// ErrUploadNotFound indicates the in-progress upload does not exist.
	ErrUploadNotFound = errors.New("staging upload not found")
	// ErrArtifactNotFound indicates no finalized immutable artifact exists for the reference.
	ErrArtifactNotFound = errors.New("finalized staging artifact not found")
	// ErrArtifactTooLarge indicates an upload exceeded its configured byte budget.
	ErrArtifactTooLarge = errors.New("staging artifact exceeds size limit")
	// ErrEmptyArtifact indicates an upload has zero bytes at finalization.
	ErrEmptyArtifact = errors.New("staging artifact is empty")
	// ErrChecksumMismatch indicates a finalized artifact's SHA-256 did not match expected.
	ErrChecksumMismatch = errors.New("staging artifact sha256 checksum mismatch")
	// ErrInvalidOffset indicates a chunk write offset did not match current upload length.
	ErrInvalidOffset = errors.New("staging chunk offset mismatch")
)

// UploadHandle identifies an in-progress staged upload before finalization.
type UploadHandle struct {
	UploadID      string    `json:"upload_id"`
	PublicationID string    `json:"publication_id"`
	MaxSizeBytes  int64     `json:"max_size_bytes"`
	CreatedAt     time.Time `json:"created_at"`
}

// FinalizedArtifact describes an immutable finalized staged archive ready for
// durable queue acceptance and helper consumption.
type FinalizedArtifact struct {
	ArtifactRef   string    `json:"artifact_ref"`
	PublicationID string    `json:"publication_id"`
	SHA256        string    `json:"sha256"`
	SizeBytes     int64     `json:"size_bytes"`
	FinalizedAt   time.Time `json:"finalized_at"`
}

// SweepResult summarizes expired partial uploads and finalized artifacts removed.
type SweepResult struct {
	ExpiredUploads   int
	ExpiredFinalized int
}

// Store persists staged image archives durably across Pod restarts.
type Store interface {
	BeginUpload(ctx context.Context, publicationID string, maxSizeBytes int64) (UploadHandle, error)
	WriteChunk(ctx context.Context, uploadID string, offset int64, r io.Reader) (int64, error)
	Finalize(ctx context.Context, uploadID string, expectedSHA256 string) (FinalizedArtifact, error)
	OpenFinalized(ctx context.Context, artifactRef string) (io.ReadCloser, FinalizedArtifact, error)
	Delete(ctx context.Context, ref string) error
	SweepExpired(ctx context.Context, now time.Time, maxUploadAge, maxFinalizedAge time.Duration) (SweepResult, error)
}
