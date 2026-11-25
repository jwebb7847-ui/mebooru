package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeText(text string) func(*os.File) error {
	return func(f *os.File) error {
		_, err := f.WriteString(text)
		return err
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil || string(b) != want {
		t.Fatalf("read %q: got %q, err=%v; want %q", path, b, err, want)
	}
}

func assertEntries(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	if !reflect.DeepEqual(got, names) {
		t.Fatalf("unexpected entries: %v; want %v", got, names)
	}
}

func TestAtomicReplacementAndFailedWriteCleanup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	if err := WriteAtomic(path, ".state.*", writeText("old")); err != nil {
		t.Fatal(err)
	}
	fail := errors.New("fixture write failure")
	if err := WriteAtomic(path, ".state.*", func(f *os.File) error {
		if _, err := f.WriteString("partial"); err != nil {
			return err
		}
		return fail
	}); !errors.Is(err, fail) {
		t.Fatalf("unexpected write result: %v", err)
	}
	assertFile(t, path, "old")
	assertEntries(t, dir, "state")
	if err := WriteAtomic(path, ".state.*", writeText("new")); err != nil {
		t.Fatal(err)
	}
	assertFile(t, path, "new")
	assertEntries(t, dir, "state")
}

func TestDurableWriteCreatesDirectoriesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "state")
	if err := WriteDurable(path, writeText("old")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	assertFile(t, path, "new")
	assertEntries(t, filepath.Dir(path), "state")
}

func TestRootDurableWriteAndFailureCleanup(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	name := filepath.Join("nested", "state")
	if err := WriteDurableIn(root, name, writeText("old")); err != nil {
		t.Fatal(err)
	}
	fail := errors.New("fixture write failure")
	if err := WriteDurableIn(root, name, func(f *os.File) error {
		if _, err := f.WriteString("partial"); err != nil {
			return err
		}
		return fail
	}); !errors.Is(err, fail) {
		t.Fatalf("unexpected write result: %v", err)
	}
	path := filepath.Join(dir, name)
	assertFile(t, path, "old")
	assertEntries(t, filepath.Dir(path), "state")
	if err := WriteDurableIn(root, name, writeText("new")); err != nil {
		t.Fatal(err)
	}
	assertFile(t, path, "new")
	assertEntries(t, filepath.Dir(path), "state")
}

func TestRootDurableWriteRefusesEscape(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "root")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside")
	if err := os.WriteFile(outside, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{filepath.Join("..", "outside"), outside} {
		if err := WriteDurableIn(root, name, func(*os.File) error {
			t.Error("escape reached write callback")
			return nil
		}); err == nil {
			t.Errorf("accepted outside path %q", name)
		}
	}
	assertFile(t, outside, "untouched")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("unexpected root entries: %v, %v", entries, err)
	}
}

func TestRootDurableWriteRefusesOutsideDirectorySymlink(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "root")
	outside := filepath.Join(parent, "outside")
	for _, p := range []string{dir, outside} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(outside, "state")
	if err := os.WriteFile(path, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := WriteDurableIn(root, filepath.Join("link", "state"), func(*os.File) error {
		t.Error("outside directory reached write callback")
		return nil
	}); err == nil {
		t.Fatal("accepted outside directory symlink")
	}
	assertFile(t, path, "untouched")
	assertEntries(t, outside, "state")
}

func TestPathAndFilenameHelpers(t *testing.T) {
	base := filepath.Join(t.TempDir(), "root")
	inside := filepath.Join(base, "nested", "state")
	outside := filepath.Join(filepath.Dir(base), "outside")
	if AbsIn(filepath.Join("nested", "state"), base) != inside ||
		AbsIn(inside, base) != inside || AbsIn("", base) != "" {
		t.Fatal("unexpected AbsIn result")
	}
	if RelIn(inside, base) != "nested/state" || RelIn(outside, base) != outside ||
		RelIn("", base) != "" {
		t.Fatal("unexpected RelIn result")
	}
	if got := SanitizeFilename(" .日本語: pony? . "); got != "日本語_ pony_" {
		t.Fatalf("unexpected sanitized name %q", got)
	}
	if got := FoldFilename("a/b\\c\x00\x7f"); got != "a_b_c__" || strings.ContainsAny(got, "/\\") {
		t.Fatalf("unexpected folded name %q", got)
	}
}
