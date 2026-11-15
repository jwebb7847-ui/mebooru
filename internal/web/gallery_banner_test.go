package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/monbooru/monbooru/internal/config"
)

func TestBannerPickerAndPresentationBoundaries(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	media := filepath.Join(root, "media")
	if err := os.Mkdir(media, 0o700); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: config.Default()}
	s.cfg.Paths.DataPath = root
	s.cfg.Galleries[0].GalleryPath = media
	image := []byte("\x89PNG\r\n\x1a\nfixture")
	inside := filepath.Join(media, "inside.png")
	for _, p := range []string{inside, filepath.Join(outside, "outside.png")} {
		if err := os.WriteFile(p, image, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(media, "outside")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if f, _, err := s.openGalleryPickerImage(r, "default", "inside.png"); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}
	if f, _, err := s.openGalleryPickerImage(r, "default", "outside/outside.png"); err == nil {
		_ = f.Close()
		t.Fatal("picker opened an image beyond the gallery")
	}
	if err := s.saveGalleryPresentation("default", galleryPresentation{FocalX: 0.25, FocalY: 0.75, Zoom: 300}); err != nil {
		t.Fatal(err)
	}
	if got := s.loadGalleryPresentation("default"); got.Zoom != 300 || got.FocalX != 0.25 || got.FocalY != 0.75 {
		t.Fatalf("saved presentation = %+v", got)
	}
	presentation := filepath.Join(root, "default", "presentation")
	if err := os.Rename(presentation, presentation+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, presentation); err != nil {
		t.Fatal(err)
	}
	if err := s.saveGalleryPresentation("default", defaultGalleryPresentation()); err == nil {
		t.Fatal("presentation was written beyond the data directory")
	}
}
