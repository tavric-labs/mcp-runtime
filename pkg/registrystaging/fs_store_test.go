package registrystaging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestFSStoreRejectsMissingOrBareTmpRoot(t *testing.T) {
	if _, err := NewFSStore(""); err == nil {
		t.Fatal("expected empty staging root to be rejected")
	}
	if _, err := NewFSStore("/tmp"); err == nil {
		t.Fatal("expected bare /tmp staging root to be rejected")
	}
}

func TestFSStoreInterruptedUploadIsNotReadableAsFinalizedAndSwept(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewFSStore(root)
	if err != nil {
		t.Fatalf("NewFSStore: %v", err)
	}

	handle, err := store.BeginUpload(ctx, "pub-interrupted", 1024)
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}
	n, err := store.WriteChunk(ctx, handle.UploadID, 0, strings.NewReader("partial-tar-bytes"))
	if err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}
	if n != int64(len("partial-tar-bytes")) {
		t.Fatalf("written = %d, want %d", n, len("partial-tar-bytes"))
	}

	// Unfinalized upload must NEVER be readable via OpenFinalized.
	if _, _, err := store.OpenFinalized(ctx, handle.UploadID); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("OpenFinalized on unfinalized upload = %v, want ErrArtifactNotFound", err)
	}

	// Sweeping with a future timestamp removes the expired partial upload.
	res, err := store.SweepExpired(ctx, time.Now().UTC().Add(2*time.Hour), time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if res.ExpiredUploads != 1 {
		t.Fatalf("ExpiredUploads = %d, want 1", res.ExpiredUploads)
	}

	// Finalizing an already swept upload fails cleanly.
	if _, err := store.Finalize(ctx, handle.UploadID, ""); !errors.Is(err, ErrUploadNotFound) {
		t.Fatalf("Finalize after sweep = %v, want ErrUploadNotFound", err)
	}
}

func TestFSStoreFinalizedArtifactSurvivesStoreRecreationAndVerifiesSHA256(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	store1, err := NewFSStore(root)
	if err != nil {
		t.Fatalf("NewFSStore #1: %v", err)
	}

	payload := []byte("deterministic-oci-archive-payload-for-pod-restart-test")
	wantHash := sha256Hex(payload)

	handle, err := store1.BeginUpload(ctx, "pub-durable-1", int64(len(payload)+64))
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}
	// Write in two chunks to exercise offset tracking.
	split := 16
	if _, err := store1.WriteChunk(ctx, handle.UploadID, 0, bytes.NewReader(payload[:split])); err != nil {
		t.Fatalf("WriteChunk #1: %v", err)
	}
	if _, err := store1.WriteChunk(ctx, handle.UploadID, int64(split), bytes.NewReader(payload[split:])); err != nil {
		t.Fatalf("WriteChunk #2: %v", err)
	}

	// Mismatched checksum must fail Finalize without publishing finalized artifact.
	if _, err := store1.Finalize(ctx, handle.UploadID, strings.Repeat("0", 64)); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Finalize wrong sha256 = %v, want ErrChecksumMismatch", err)
	}

	finalized, err := store1.Finalize(ctx, handle.UploadID, wantHash)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if finalized.SHA256 != wantHash {
		t.Fatalf("SHA256 = %q, want %q", finalized.SHA256, wantHash)
	}
	if finalized.SizeBytes != int64(len(payload)) {
		t.Fatalf("SizeBytes = %d, want %d", finalized.SizeBytes, len(payload))
	}
	if finalized.ArtifactRef == "" {
		t.Fatal("expected non-empty ArtifactRef")
	}

	// Simulate Pod replacement by creating a brand new FSStore on the same durable root.
	store2, err := NewFSStore(root)
	if err != nil {
		t.Fatalf("NewFSStore #2: %v", err)
	}
	rc, meta, err := store2.OpenFinalized(ctx, finalized.ArtifactRef)
	if err != nil {
		t.Fatalf("OpenFinalized after restart: %v", err)
	}
	defer rc.Close()

	gotBytes, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(gotBytes, payload) {
		t.Fatalf("payload mismatch: got %q, want %q", gotBytes, payload)
	}
	if meta.SHA256 != wantHash || sha256Hex(gotBytes) != wantHash {
		t.Fatalf("verified sha256 mismatch: meta=%q actual=%q want=%q", meta.SHA256, sha256Hex(gotBytes), wantHash)
	}

	// Deleting removes the finalized artifact cleanly.
	if err := store2.Delete(ctx, finalized.ArtifactRef); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := store2.OpenFinalized(ctx, finalized.ArtifactRef); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("OpenFinalized after delete = %v, want ErrArtifactNotFound", err)
	}
}

func TestFSStoreEnforcesMaxSizeAndRejectsPathTraversal(t *testing.T) {
	ctx := context.Background()
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSStore: %v", err)
	}

	handle, err := store.BeginUpload(ctx, "pub-size", 8)
	if err != nil {
		t.Fatalf("BeginUpload: %v", err)
	}
	if _, err := store.WriteChunk(ctx, handle.UploadID, 0, strings.NewReader("123456789")); !errors.Is(err, ErrArtifactTooLarge) {
		t.Fatalf("WriteChunk oversize = %v, want ErrArtifactTooLarge", err)
	}
	if _, _, err := store.OpenFinalized(ctx, "../escape"); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("OpenFinalized traversal = %v, want ErrInvalidReference", err)
	}
}
