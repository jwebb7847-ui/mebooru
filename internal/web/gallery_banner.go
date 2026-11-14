package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/monbooru/monbooru/internal/shared/fsx"
)

const (
	maxGalleryBannerBytes int64 = 12 << 20 // 12 MiB
	minGalleryBannerZoom        = 100
	maxGalleryBannerZoom        = 300
)

var galleryBannerTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
}

type galleryPresentation struct {
	FocalX float64 `json:"focal_x"`
	FocalY float64 `json:"focal_y"`
	Zoom   int     `json:"zoom_percent"`
}

func defaultGalleryPresentation() galleryPresentation {
	return galleryPresentation{
		FocalX: 0.5,
		FocalY: 0.5,
		Zoom:   minGalleryBannerZoom,
	}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func (s *Server) galleryBannerPath(name string) (string, bool) {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()

	if s.cfg.FindGallery(name) == nil {
		return "", false
	}

	return filepath.Join(
		s.cfg.Paths.DataPath,
		name,
		"presentation",
		"banner",
	), true
}

func (s *Server) galleryPresentationPath(name string) (string, bool) {
	bannerPath, ok := s.galleryBannerPath(name)
	if !ok {
		return "", false
	}

	return filepath.Join(filepath.Dir(bannerPath), "presentation.json"), true
}

func (s *Server) galleryBannerExists(name string) bool {
	path, ok := s.galleryBannerPath(name)
	if !ok {
		return false
	}

	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func (s *Server) loadGalleryPresentation(name string) galleryPresentation {
	presentation := defaultGalleryPresentation()

	path, ok := s.galleryPresentationPath(name)
	if !ok {
		return presentation
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return presentation
	}

	if err := json.Unmarshal(data, &presentation); err != nil {
		return defaultGalleryPresentation()
	}

	presentation.FocalX = clamp01(presentation.FocalX)
	presentation.FocalY = clamp01(presentation.FocalY)
	presentation.Zoom = max(minGalleryBannerZoom, min(maxGalleryBannerZoom, presentation.Zoom))

	return presentation
}

func (s *Server) saveGalleryPresentation(name string, presentation galleryPresentation) error {
	path, ok := s.galleryPresentationPath(name)
	if !ok {
		return fmt.Errorf("unknown gallery %q", name)
	}

	presentation.FocalX = clamp01(presentation.FocalX)
	presentation.FocalY = clamp01(presentation.FocalY)

	data, err := json.MarshalIndent(presentation, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	return fsx.WriteDurable(path, func(f *os.File) error {
		_, err := f.Write(data)
		return err
	})
}

func (s *Server) galleryBannerURL(name string) string {
	if !s.galleryBannerExists(name) {
		return ""
	}

	return "/internal/gallery/banner/" + name
}

func (presentation galleryPresentation) Position() string {
	return fmt.Sprintf(
		"%.2f%% %.2f%%",
		presentation.FocalX*100,
		presentation.FocalY*100,
	)
}

func (presentation galleryPresentation) Scale() float64 {
	return float64(presentation.Zoom) / 100
}

func (s *Server) galleryBannerGet(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))

	path, ok := s.galleryBannerPath(name)
	if !ok {
		http.NotFound(w, r)
		return
	}

	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}

	head := make([]byte, 512)
	n, err := f.Read(head)
	if err != nil && !errors.Is(err, io.EOF) {
		http.NotFound(w, r)
		return
	}

	contentType := http.DetectContentType(head[:n])
	if !galleryBannerTypes[contentType] {
		http.NotFound(w, r)
		return
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", contentType)
	setGalleryScopedCache(w, name, "banner", path)
	http.ServeContent(w, r, "banner", info.ModTime(), f)
}

func (s *Server) settingsGalleryBannerPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxGalleryBannerBytes+(1<<20))

	if err := r.ParseMultipartForm(maxGalleryBannerBytes); err != nil {
		writeInlineFlash(w, "err", "Banner upload is too large or invalid.")
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))

	path, ok := s.galleryBannerPath(name)
	if !ok {
		writeInlineFlash(w, "err", fmt.Sprintf("Unknown gallery %q.", name))
		return
	}

	file, _, err := r.FormFile("banner")
	if err != nil {
		writeInlineFlash(w, "err", "Choose a banner image first.")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxGalleryBannerBytes+1))
	if err != nil {
		writeInlineFlash(w, "err", "Could not read banner image.")
		return
	}

	if len(data) == 0 {
		writeInlineFlash(w, "err", "Banner image is empty.")
		return
	}

	if int64(len(data)) > maxGalleryBannerBytes {
		writeInlineFlash(w, "err", "Banner image exceeds the 12 MiB limit.")
		return
	}

	head := data
	if len(head) > 512 {
		head = head[:512]
	}

	contentType := http.DetectContentType(head)
	if !galleryBannerTypes[contentType] {
		writeInlineFlash(w, "err", "Banner must be JPEG, PNG, or WebP.")
		return
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		writeInlineFlash(w, "err", "Could not create gallery presentation folder.")
		return
	}

	if err := fsx.WriteDurable(path, func(f *os.File) error {
		_, err := f.Write(data)
		return err
	}); err != nil {
		writeInlineFlash(w, "err", "Could not save gallery banner.")
		return
	}

	// A new banner starts centered. The user can reposition it afterward.
	if err := s.saveGalleryPresentation(name, defaultGalleryPresentation()); err != nil {
		_ = os.Remove(path)
		writeInlineFlash(w, "err", "Could not initialize gallery banner position.")
		return
	}

	w.Header().Set("HX-Refresh", "true")
	writeInlineFlash(w, "ok", "Banner updated for "+name+".")
}

func (s *Server) settingsGalleryBannerFocalPost(w http.ResponseWriter, r *http.Request) {
	if !parseFormOK(w, r) {
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))

	if !s.galleryBannerExists(name) {
		writeInlineFlash(w, "err", "This gallery does not have a banner.")
		return
	}

	x, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("focal_x")), 64)
	if err != nil {
		writeInlineFlash(w, "err", "Invalid horizontal banner position.")
		return
	}

	y, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("focal_y")), 64)
	if err != nil {
		writeInlineFlash(w, "err", "Invalid vertical banner position.")
		return
	}

	presentation := s.loadGalleryPresentation(name)
	presentation.FocalX = x
	presentation.FocalY = y
	if value := strings.TrimSpace(r.FormValue("zoom_percent")); value != "" {
		zoom, err := strconv.Atoi(value)
		if err != nil || zoom < minGalleryBannerZoom || zoom > maxGalleryBannerZoom {
			writeInlineFlash(w, "err", "Invalid banner zoom.")
			return
		}
		presentation.Zoom = zoom
	}

	if err := s.saveGalleryPresentation(name, presentation); err != nil {
		writeInlineFlash(w, "err", "Could not save banner framing.")
		return
	}

	w.Header().Set("HX-Refresh", "true")
	writeInlineFlash(w, "ok", "Banner framing saved for "+name+".")
}

func (s *Server) settingsGalleryBannerClearPost(w http.ResponseWriter, r *http.Request) {
	if !parseFormOK(w, r) {
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))

	bannerPath, ok := s.galleryBannerPath(name)
	if !ok {
		writeInlineFlash(w, "err", fmt.Sprintf("Unknown gallery %q.", name))
		return
	}

	presentationPath, _ := s.galleryPresentationPath(name)

	if err := os.Remove(bannerPath); err != nil && !os.IsNotExist(err) {
		writeInlineFlash(w, "err", "Could not remove gallery banner.")
		return
	}

	if presentationPath != "" {
		_ = os.Remove(presentationPath)
	}

	_ = os.Remove(filepath.Dir(bannerPath))

	w.Header().Set("HX-Refresh", "true")
	writeInlineFlash(w, "ok", "Banner cleared for "+name+".")
}
