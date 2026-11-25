package compile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ReadSnapshotFile loads a snapshot written by WriteSnapshotFile and verifies version and hash.
func ReadSnapshotFile(path string) (*Snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", path, err)
	}
	if s.Version != SnapshotVersion {
		return nil, fmt.Errorf("snapshot %s: version %d, want %d", path, s.Version, SnapshotVersion)
	}
	if got := hashOf(&s); got != s.Hash {
		return nil, fmt.Errorf("snapshot %s: hash mismatch (file edited by hand?)", path)
	}
	return &s, nil
}

// WriteSnapshotFile writes the snapshot atomically (temp file + rename) so the engine never reads a
// half-written file.
func WriteSnapshotFile(path string, s *Snapshot) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".snapshot-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := writeSynced(tmp, append(b, '\n')); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// writeSynced writes b to f and flushes it to the disk; f is closed when that fails.
func writeSynced(f *os.File, b []byte) error {
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return nil
}
