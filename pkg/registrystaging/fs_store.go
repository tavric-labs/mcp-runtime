package registrystaging

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	incomingDirName  = "incoming"
	finalizedDirName = "finalized"
	defaultMaxSize   = int64(2) << 30 // 2 GiB
)

var (
	safeIDPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	safeSHA256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	finalizedRefPattern = regexp.MustCompile(`^finalized/([A-Za-z0-9][A-Za-z0-9._-]{0,127})/([0-9a-f]{64})\.tar$`)
)

type uploadMeta struct {
	UploadID      string    `json:"upload_id"`
	PublicationID string    `json:"publication_id"`
	MaxSizeBytes  int64     `json:"max_size_bytes"`
	CreatedAt     time.Time `json:"created_at"`
}

// FSStore implements Store on an explicit durable filesystem/PV staging root.
type FSStore struct {
	root         string
	incomingDir  string
	finalizedDir string
}

// NewFSStore initializes an FSStore backed by root. root must be an explicit
// non-empty directory path and must not be bare /tmp.
func NewFSStore(root string) (*FSStore, error) {
	cleaned := filepath.Clean(strings.TrimSpace(root))
	if cleaned == "" || cleaned == "." || cleaned == "/" || cleaned == "/tmp" || cleaned == filepath.Clean(os.TempDir()) {
		return nil, fmt.Errorf("%w: staging root must be an explicit durable directory", ErrInvalidConfig)
	}
	incomingDir := filepath.Join(cleaned, incomingDirName)
	finalizedDir := filepath.Join(cleaned, finalizedDirName)
	if err := os.MkdirAll(incomingDir, 0o750); err != nil {
		return nil, fmt.Errorf("create staging incoming dir: %w", err)
	}
	if err := os.MkdirAll(finalizedDir, 0o750); err != nil {
		return nil, fmt.Errorf("create staging finalized dir: %w", err)
	}
	return &FSStore{
		root:         cleaned,
		incomingDir:  incomingDir,
		finalizedDir: finalizedDir,
	}, nil
}

// BeginUpload allocates an isolated incoming upload slot for publicationID.
func (s *FSStore) BeginUpload(ctx context.Context, publicationID string, maxSizeBytes int64) (UploadHandle, error) {
	if err := ctx.Err(); err != nil {
		return UploadHandle{}, err
	}
	publicationID = strings.TrimSpace(publicationID)
	if !safeIDPattern.MatchString(publicationID) {
		return UploadHandle{}, fmt.Errorf("%w: invalid publication_id", ErrInvalidReference)
	}
	if maxSizeBytes <= 0 {
		maxSizeBytes = defaultMaxSize
	}
	token, err := randomHex(16)
	if err != nil {
		return UploadHandle{}, fmt.Errorf("generate upload id: %w", err)
	}
	uploadID := "upl-" + token
	now := time.Now().UTC()
	meta := uploadMeta{
		UploadID:      uploadID,
		PublicationID: publicationID,
		MaxSizeBytes:  maxSizeBytes,
		CreatedAt:     now,
	}
	partRel := incomingPartRel(uploadID)
	rootFS, err := os.OpenRoot(s.root)
	if err != nil {
		return UploadHandle{}, fmt.Errorf("open staging root: %w", err)
	}
	defer rootFS.Close()
	f, err := rootFS.OpenFile(partRel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return UploadHandle{}, fmt.Errorf("create upload part file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = rootFS.Remove(partRel)
		return UploadHandle{}, fmt.Errorf("close upload part file: %w", err)
	}
	if err := writeJSONAtomic(s.incomingMetaPath(uploadID), meta); err != nil {
		_ = rootFS.Remove(partRel)
		return UploadHandle{}, fmt.Errorf("write upload metadata: %w", err)
	}
	return UploadHandle{
		UploadID:      uploadID,
		PublicationID: publicationID,
		MaxSizeBytes:  maxSizeBytes,
		CreatedAt:     now,
	}, nil
}

// WriteChunk appends bytes from r at offset into an in-progress upload while
// enforcing the upload's byte budget.
func (s *FSStore) WriteChunk(ctx context.Context, uploadID string, offset int64, r io.Reader) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !safeIDPattern.MatchString(uploadID) {
		return 0, fmt.Errorf("%w: invalid upload_id", ErrInvalidReference)
	}
	meta, err := s.readUploadMeta(uploadID)
	if err != nil {
		return 0, err
	}
	rootFS, err := os.OpenRoot(s.root)
	if err != nil {
		return 0, fmt.Errorf("open staging root: %w", err)
	}
	defer rootFS.Close()
	f, err := rootFS.OpenFile(incomingPartRel(uploadID), os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, ErrUploadNotFound
		}
		return 0, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if offset != info.Size() {
		return 0, fmt.Errorf("%w: got %d, want %d", ErrInvalidOffset, offset, info.Size())
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}

	remaining := meta.MaxSizeBytes - offset
	if remaining < 0 {
		return 0, ErrArtifactTooLarge
	}
	// Read up to remaining + 1 bytes to detect overflow cleanly.
	limited := io.LimitReader(r, remaining+1)
	written, err := io.Copy(f, limited)
	if err != nil {
		return written, err
	}
	if written > remaining {
		_ = f.Truncate(offset)
		return 0, ErrArtifactTooLarge
	}
	if err := f.Sync(); err != nil {
		return written, err
	}
	return written, nil
}

// Finalize verifies size and SHA-256 of an in-progress upload, fsyncs it, and
// atomically moves it into the immutable finalized directory.
func (s *FSStore) Finalize(ctx context.Context, uploadID string, expectedSHA256 string) (FinalizedArtifact, error) {
	if err := ctx.Err(); err != nil {
		return FinalizedArtifact{}, err
	}
	if !safeIDPattern.MatchString(uploadID) {
		return FinalizedArtifact{}, fmt.Errorf("%w: invalid upload_id", ErrInvalidReference)
	}
	expectedSHA256 = strings.ToLower(strings.TrimSpace(expectedSHA256))
	if expectedSHA256 != "" && !safeSHA256Pattern.MatchString(expectedSHA256) {
		return FinalizedArtifact{}, fmt.Errorf("%w: invalid sha256", ErrInvalidReference)
	}
	meta, err := s.readUploadMeta(uploadID)
	if err != nil {
		return FinalizedArtifact{}, err
	}

	rootFS, err := os.OpenRoot(s.root)
	if err != nil {
		return FinalizedArtifact{}, fmt.Errorf("open staging root: %w", err)
	}
	defer rootFS.Close()
	partPath := s.incomingPartPath(uploadID)
	f, err := rootFS.Open(incomingPartRel(uploadID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return FinalizedArtifact{}, ErrUploadNotFound
		}
		return FinalizedArtifact{}, err
	}
	hasher := sha256.New()
	size, copyErr := io.Copy(hasher, f)
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil {
		return FinalizedArtifact{}, copyErr
	}
	if syncErr != nil {
		return FinalizedArtifact{}, syncErr
	}
	if closeErr != nil {
		return FinalizedArtifact{}, closeErr
	}
	if size == 0 {
		return FinalizedArtifact{}, ErrEmptyArtifact
	}
	if size > meta.MaxSizeBytes {
		return FinalizedArtifact{}, ErrArtifactTooLarge
	}
	actualSHA := hex.EncodeToString(hasher.Sum(nil))
	if expectedSHA256 != "" && actualSHA != expectedSHA256 {
		return FinalizedArtifact{}, fmt.Errorf("%w: got %s, want %s", ErrChecksumMismatch, actualSHA, expectedSHA256)
	}

	pubDir := filepath.Join(s.finalizedDir, meta.PublicationID)
	if err := os.MkdirAll(pubDir, 0o750); err != nil {
		return FinalizedArtifact{}, fmt.Errorf("create finalized publication dir: %w", err)
	}
	artifactRef := fmt.Sprintf("%s/%s/%s.tar", finalizedDirName, meta.PublicationID, actualSHA)
	finalTarPath := filepath.Join(pubDir, actualSHA+".tar")
	finalMetaPath := filepath.Join(pubDir, actualSHA+".meta.json")
	artifact := FinalizedArtifact{
		ArtifactRef:   artifactRef,
		PublicationID: meta.PublicationID,
		SHA256:        actualSHA,
		SizeBytes:     size,
		FinalizedAt:   time.Now().UTC(),
	}

	// Write metadata atomically before renaming the payload so any reader
	// seeing the finalized tar also sees valid metadata.
	if err := writeJSONAtomic(finalMetaPath, artifact); err != nil {
		return FinalizedArtifact{}, fmt.Errorf("write finalized metadata: %w", err)
	}
	if err := os.Rename(partPath, finalTarPath); err != nil {
		_ = os.Remove(finalMetaPath)
		return FinalizedArtifact{}, fmt.Errorf("atomic finalize rename: %w", err)
	}
	_ = syncDir(pubDir)
	_ = os.Remove(s.incomingMetaPath(uploadID))
	_ = syncDir(s.incomingDir)
	return artifact, nil
}

// OpenFinalized opens an immutable finalized artifact by ArtifactRef. Unfinalized
// uploads or invalid paths are never opened.
func (s *FSStore) OpenFinalized(ctx context.Context, artifactRef string) (io.ReadCloser, FinalizedArtifact, error) {
	if err := ctx.Err(); err != nil {
		return nil, FinalizedArtifact{}, err
	}
	pubID, sha, err := parseFinalizedRef(artifactRef)
	if err != nil {
		// Distinguish traversal/malformed refs from bare upload IDs that were never finalized.
		if safeIDPattern.MatchString(strings.TrimSpace(artifactRef)) {
			return nil, FinalizedArtifact{}, ErrArtifactNotFound
		}
		return nil, FinalizedArtifact{}, err
	}
	rootFS, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, FinalizedArtifact{}, fmt.Errorf("open staging root: %w", err)
	}
	defer rootFS.Close()
	tarRel := filepath.Join(finalizedDirName, pubID, sha+".tar")
	metaRel := filepath.Join(finalizedDirName, pubID, sha+".meta.json")

	rawMeta, err := rootFS.ReadFile(metaRel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, FinalizedArtifact{}, ErrArtifactNotFound
		}
		return nil, FinalizedArtifact{}, err
	}
	var artifact FinalizedArtifact
	if err := json.Unmarshal(rawMeta, &artifact); err != nil {
		return nil, FinalizedArtifact{}, fmt.Errorf("decode finalized metadata: %w", err)
	}
	f, err := rootFS.Open(tarRel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, FinalizedArtifact{}, ErrArtifactNotFound
		}
		return nil, FinalizedArtifact{}, err
	}
	return f, artifact, nil
}

// Delete removes either a finalized artifact (by ArtifactRef) or an in-progress
// upload (by uploadID).
func (s *FSStore) Delete(ctx context.Context, ref string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ref = strings.TrimSpace(ref)
	if pubID, sha, err := parseFinalizedRef(ref); err == nil {
		pubDir := filepath.Join(s.finalizedDir, pubID)
		_ = removeIfExists(filepath.Join(pubDir, sha+".tar"))
		_ = removeIfExists(filepath.Join(pubDir, sha+".meta.json"))
		_ = os.Remove(pubDir) // remove directory if empty
		return nil
	}
	if safeIDPattern.MatchString(ref) {
		_ = removeIfExists(s.incomingPartPath(ref))
		_ = removeIfExists(s.incomingMetaPath(ref))
		return nil
	}
	return ErrInvalidReference
}

// SweepExpired removes interrupted uploads older than maxUploadAge and
// finalized artifacts older than maxFinalizedAge relative to now.
func (s *FSStore) SweepExpired(ctx context.Context, now time.Time, maxUploadAge, maxFinalizedAge time.Duration) (SweepResult, error) {
	if err := ctx.Err(); err != nil {
		return SweepResult{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var result SweepResult

	if maxUploadAge > 0 {
		entries, err := os.ReadDir(s.incomingDir)
		if err != nil {
			return result, err
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".meta.json") {
				continue
			}
			uploadID := strings.TrimSuffix(entry.Name(), ".meta.json")
			meta, err := s.readUploadMeta(uploadID)
			if err != nil {
				continue
			}
			if now.Sub(meta.CreatedAt) >= maxUploadAge {
				_ = removeIfExists(s.incomingPartPath(uploadID))
				_ = removeIfExists(s.incomingMetaPath(uploadID))
				result.ExpiredUploads++
			}
		}
	}

	if maxFinalizedAge > 0 {
		pubDirs, err := os.ReadDir(s.finalizedDir)
		if err != nil {
			return result, err
		}
		rootFS, err := os.OpenRoot(s.root)
		if err != nil {
			return result, err
		}
		defer rootFS.Close()
		for _, dir := range pubDirs {
			if !dir.IsDir() || !safeIDPattern.MatchString(dir.Name()) {
				continue
			}
			pubPath := filepath.Join(s.finalizedDir, dir.Name())
			files, err := os.ReadDir(pubPath)
			if err != nil {
				continue
			}
			for _, f := range files {
				if !strings.HasSuffix(f.Name(), ".meta.json") {
					continue
				}
				sha := strings.TrimSuffix(f.Name(), ".meta.json")
				if !safeSHA256Pattern.MatchString(sha) {
					continue
				}
				raw, err := rootFS.ReadFile(filepath.Join(finalizedDirName, dir.Name(), f.Name()))
				if err != nil {
					continue
				}
				var artifact FinalizedArtifact
				if err := json.Unmarshal(raw, &artifact); err != nil {
					continue
				}
				if now.Sub(artifact.FinalizedAt) >= maxFinalizedAge {
					_ = removeIfExists(filepath.Join(pubPath, sha+".tar"))
					_ = removeIfExists(filepath.Join(pubPath, f.Name()))
					result.ExpiredFinalized++
				}
			}
			_ = os.Remove(pubPath)
		}
	}

	return result, nil
}

func incomingPartRel(uploadID string) string {
	return filepath.Join(incomingDirName, uploadID+".part")
}

func incomingMetaRel(uploadID string) string {
	return filepath.Join(incomingDirName, uploadID+".meta.json")
}

func (s *FSStore) incomingPartPath(uploadID string) string {
	return filepath.Join(s.incomingDir, uploadID+".part")
}

func (s *FSStore) incomingMetaPath(uploadID string) string {
	return filepath.Join(s.incomingDir, uploadID+".meta.json")
}

func (s *FSStore) readUploadMeta(uploadID string) (uploadMeta, error) {
	rootFS, err := os.OpenRoot(s.root)
	if err != nil {
		return uploadMeta{}, fmt.Errorf("open staging root: %w", err)
	}
	defer rootFS.Close()
	raw, err := rootFS.ReadFile(incomingMetaRel(uploadID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return uploadMeta{}, ErrUploadNotFound
		}
		return uploadMeta{}, err
	}
	var meta uploadMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return uploadMeta{}, fmt.Errorf("decode upload metadata: %w", err)
	}
	return meta, nil
}

func parseFinalizedRef(ref string) (pubID, sha string, err error) {
	matches := finalizedRefPattern.FindStringSubmatch(strings.TrimSpace(ref))
	if len(matches) != 3 {
		return "", "", ErrInvalidReference
	}
	return matches[1], matches[2], nil
}

func writeJSONAtomic(target string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".meta-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		cleanup()
		return err
	}
	_ = syncDir(dir)
	return nil
}

func syncDir(path string) error {
	rootFS, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer rootFS.Close()
	dir, err := rootFS.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
