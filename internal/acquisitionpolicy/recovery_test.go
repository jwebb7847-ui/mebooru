package acquisitionpolicy

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecoverStaleReceiptAfterReboot(t *testing.T) {
	dir := t.TempDir()
	stale := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	lock, err := LockProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Model an armed generation whose library and custodian have exited.
	if _, err := lock.BeginWorker(); err != nil {
		t.Fatal(err)
	}
	if err := lock.file.Close(); err != nil {
		t.Fatal(err)
	}
	defer lock.root.Close()
	if err := RecordCustodyStopped(dir, stale); err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(dir, cleanupReceiptFile)
	original, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := recoverAfterReboot(dir, now.Add(-time.Hour)); !errors.Is(err, errRebootRequired) {
		t.Fatalf("accepted recovery within the same boot: %v", err)
	}
	if err := recoverAfterReboot(dir, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	opened, err := LockProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if err := opened.checkRecovery(); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(receipt); err != nil || string(body) != string(original) {
		t.Fatalf("previous receipt changed: %v", err)
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if offline, err := store.Load(); err != nil || !offline {
		t.Fatalf("offline=%v: %v", offline, err)
	}
}
