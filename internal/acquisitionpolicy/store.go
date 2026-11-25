// Package acquisitionpolicy owns the persisted network consent for the
// future managed acquisition worker. It is deliberately library-side:
// worker configuration, pairing and queue state cannot grant online access.
package acquisitionpolicy

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jwebb7847-ui/mebooru/shared/fsx"
)

const (
	policyFile = "acquisition-policy"
	offlineRecord = "mebooru-acquisition-policy-v1\noffline\n"
	onlineRecord = "mebooru-acquisition-policy-v1\nonline\n"
	maxRecordBytes = 128
)

var ErrInvalidPolicy = errors.New("invalid persisted acquisition policy")

// Store holds the library-owned profile root. The caller must ensure
// exclusive profile ownership; this is not a cross-process lock or sandbox.
// Opening a store has no network, worker-launch or configuration side effect.
type Store struct { root *os.Root }

func Open(profileDir string) (*Store, error) {
	if !filepath.IsAbs(profileDir) {
		return nil, errors.New("acquisition policy requires an absolute profile directory")
	}
	root, err := os.OpenRoot(profileDir)
	if err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

func (s *Store) Close() error { return s.root.Close() }

// Load returns offline on EVERY read failure, including an absent file.
// Only the exact explicit online record grants online permission. Absence
// is the safe first-run default; malformed/unreadable files return an error
// as well so the library can report a fault without disabling local browsing.
func (s *Store) Load() (offline bool, err error) {
	info, err := s.root.Lstat(policyFile)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return true, fmt.Errorf("read acquisition policy: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxRecordBytes {
		return true, ErrInvalidPolicy
	}
	f, err := s.root.Open(policyFile)
	if err != nil {
		return true, fmt.Errorf("open acquisition policy: %w", err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return true, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return true, ErrInvalidPolicy
	}
	body, err := io.ReadAll(io.LimitReader(f, maxRecordBytes+1))
	if err != nil {
		return true, fmt.Errorf("read acquisition policy: %w", err)
	}
	switch string(body) {
	case offlineRecord:
		return true, nil
	case onlineRecord:
		return false, nil
	default:
		return true, ErrInvalidPolicy
	}
}

// Save is an explicit library-side policy change. Reuse the existing
// rooted atomic, file-synced writer (directory fsync is best effort).
// Refuse nonregular existing entries rather than silently replace them.
// The exclusive profile owner serializes writes and live worker transitions.
func (s *Store) Save(offline bool) error {
	info, err := s.root.Lstat(policyFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && !info.Mode().IsRegular() {
		return ErrInvalidPolicy
	}
	record := offlineRecord
	if !offline {
		record = onlineRecord
	}
	return fsx.WriteDurableIn(s.root, policyFile, func(f *os.File) error {
		_, err := io.WriteString(f, record)
		return err
	})
}
