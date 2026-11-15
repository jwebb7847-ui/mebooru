package galleryio

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monbooru/monbooru/internal/db"
	"github.com/monbooru/monbooru/internal/gallery"
)

func TestArchiveRestoreKeepsWritesWithinGallery(t *testing.T) {
	media, outside := t.TempDir(), t.TempDir()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("art/pony.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("restored")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := []translatedFile{{rel: zr.File[0].Name, file: zr.File[0]}}
	b := gallery.NewBoundary(media, nil, nil)
	link := filepath.Join(media, "art")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := planExtraction(b, files, 1024); err == nil {
		t.Fatal("archive plan accepted a path outside the gallery")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	dsts, err := planExtraction(b, files, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := extractPlanned(b, files, dsts, 1024); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "pony.png")); !os.IsNotExist(err) {
		t.Fatalf("unexpected file beyond gallery: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(media, "art", "pony.png")); err != nil || string(got) != "restored" {
		t.Fatalf("restored in-gallery file = %q, %v", got, err)
	}
}

func TestRestoreRebasesCanonicalAndAliasPaths(t *testing.T) {
	root := t.TempDir()
	database, err := db.Open(filepath.Join(t.TempDir(), "backup.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Bootstrap(database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Write.Exec(`INSERT INTO images (id, sha256, canonical_path, folder_path, file_type, file_size, source)
		VALUES (1, ?, '/old/gallery/art/pony.png', 'art', 'png', 1, 'https://example.invalid/post/1')`, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Write.Exec(`INSERT INTO image_paths (id, image_id, path, is_canonical)
		VALUES (1, 1, '/old/gallery/art/pony.png', 1), (2, 1, '/old/gallery/copies/pony.png', 0)`); err != nil {
		t.Fatal(err)
	}
	if err := rebaseImagePaths(database, gallery.NewBoundary(root, nil, nil)); err != nil {
		t.Fatal(err)
	}
	var canonical, alias, source string
	var missing int
	if err := database.Read.QueryRow(`SELECT canonical_path, is_missing, source FROM images WHERE id=1`).Scan(&canonical, &missing, &source); err != nil {
		t.Fatal(err)
	}
	if err := database.Read.QueryRow(`SELECT path FROM image_paths WHERE id=2`).Scan(&alias); err != nil {
		t.Fatal(err)
	}
	if canonical != filepath.Join(root, "art", "pony.png") || alias != filepath.Join(root, "copies", "pony.png") || missing != 1 || source != "https://example.invalid/post/1" {
		t.Fatalf("restored canonical=%q alias=%q missing=%d source=%q", canonical, alias, missing, source)
	}
}

func TestRestoreInvalidPathsLeaveExistingGalleryAlone(t *testing.T) {
	media, data := t.TempDir(), t.TempDir()
	file := filepath.Join(media, "keep.png")
	live := filepath.Join(data, "gallery.db")
	for _, target := range []string{file, live} {
		if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ name, document string }{
		{"light", `{"version":1,"images":[{"sha256":"abc","path":"../unavailable/pony.png"}]}`},
		{"full", `{"version":15,"gallery_name":"restored","images":[{"id":1,"sha256":"abc","canonical_path":"/old/pony.png","folder_path":"../unavailable","file_type":"png","file_size":1}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backup := filepath.Join(data, tc.name+".json")
			if err := os.WriteFile(backup, []byte(tc.document), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := ApplyImport("json", backup, live, filepath.Join(data, "thumbs"), gallery.NewBoundary(media, nil, nil), 1, func() error {
				t.Fatal("invalid backup reached the gallery replacement step")
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), "path") && !strings.Contains(err.Error(), "manifest") {
				t.Fatalf("expected path validation error, got %v", err)
			}
			for _, target := range []string{file, live} {
				if got, err := os.ReadFile(target); err != nil || string(got) != "keep" {
					t.Fatalf("existing %s changed: %q, %v", target, got, err)
				}
			}
		})
	}
}
