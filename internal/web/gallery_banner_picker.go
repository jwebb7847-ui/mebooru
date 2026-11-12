package web

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/monbooru/monbooru/internal/shared/fsx"
)

const maxGalleryBannerPickerEntries = 500

type galleryBannerPickerEntry struct {
	Name       string
	Rel        string
	IsDir      bool
	PreviewURL string
}

type galleryBannerPickerData struct {
	GalleryName string
	Current     string
	Parent      string
	AtRoot      bool
	Entries     []galleryBannerPickerEntry
	Truncated   bool
}

func cleanGalleryPickerRel(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	raw = strings.ReplaceAll(raw, "\\", "/")
	raw = strings.TrimPrefix(raw, "/")

	if raw == "" || raw == "." {
		return "", true
	}

	for _, part := range strings.Split(raw, "/") {
		if part == ".." {
			return "", false
		}
	}

	cleaned := pathpkg.Clean(raw)

	if cleaned == "." {
		return "", true
	}

	if pathpkg.IsAbs(cleaned) ||
		cleaned == ".." ||
		strings.HasPrefix(cleaned, "../") {
		return "", false
	}

	return cleaned, true
}

func pathEscapesRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return true
	}

	return rel == ".." ||
		strings.HasPrefix(
			rel,
			".."+string(filepath.Separator),
		)
}

func (s *Server) galleryPickerRoot(
	name string,
) (string, bool) {
	s.cfgMu.RLock()
	g := s.cfg.FindGallery(name)

	var galleryPath string
	if g != nil {
		galleryPath = g.GalleryPath
	}
	s.cfgMu.RUnlock()

	if galleryPath == "" {
		return "", false
	}

	root, err := filepath.Abs(galleryPath)
	if err != nil {
		return "", false
	}

	root = filepath.Clean(root)

	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}

	return real, true
}

// galleryPickerScope determines how much of the filesystem this request
// may browse.
//
// Remote/LAN requests are confined to the selected gallery.
//
// A local desktop request may browse upward from the gallery. When the
// gallery is beneath the user's home directory, home becomes the browser
// root. This gives macOS and Windows users convenient access to Desktop,
// Pictures, Documents, etc. without exposing the entire filesystem.
func (s *Server) galleryPickerScope(
	r *http.Request,
	name string,
) (
	browseRoot string,
	galleryRoot string,
	startRel string,
	ok bool,
) {
	galleryRoot, ok = s.galleryPickerRoot(name)
	if !ok {
		return "", "", "", false
	}

	browseRoot = galleryRoot

	if !s.desktopLocal(r) {
		return browseRoot, galleryRoot, "", true
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return browseRoot, galleryRoot, "", true
	}

	home, err = filepath.Abs(home)
	if err != nil {
		return browseRoot, galleryRoot, "", true
	}

	home = filepath.Clean(home)

	if realHome, err := filepath.EvalSymlinks(home); err == nil {
		home = realHome
	}

	if pathEscapesRoot(home, galleryRoot) {
		// Gallery lives somewhere outside the user's home directory.
		// Keep the conservative gallery-root boundary for now.
		return browseRoot, galleryRoot, "", true
	}

	browseRoot = home

	rel, err := filepath.Rel(browseRoot, galleryRoot)
	if err != nil {
		return galleryRoot, galleryRoot, "", true
	}

	if rel == "." {
		rel = ""
	}

	startRel = filepath.ToSlash(rel)

	return browseRoot, galleryRoot, startRel, true
}

func (s *Server) galleryPickerResolve(
	r *http.Request,
	name string,
	rawRel string,
) (
	root string,
	rel string,
	full string,
	ok bool,
) {
	root, _, _, ok = s.galleryPickerScope(r, name)
	if !ok {
		return "", "", "", false
	}

	rel, ok = cleanGalleryPickerRel(rawRel)
	if !ok {
		return "", "", "", false
	}

	osRel := filepath.FromSlash(rel)

	if filepath.IsAbs(osRel) ||
		filepath.VolumeName(osRel) != "" {
		return "", "", "", false
	}

	full = filepath.Clean(
		filepath.Join(root, osRel),
	)

	if pathEscapesRoot(root, full) {
		return "", "", "", false
	}

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", "", false
	}

	realFull, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", "", "", false
	}

	if pathEscapesRoot(realRoot, realFull) {
		return "", "", "", false
	}

	return realRoot, rel, realFull, true
}

func bannerPickerImageExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".webp":
		return true
	default:
		return false
	}
}

func (s *Server) galleryBannerPickerGet(
	w http.ResponseWriter,
	r *http.Request,
) {
	name := strings.TrimSpace(
		r.PathValue("name"),
	)

	_, _, startRel, ok :=
		s.galleryPickerScope(r, name)

	if !ok {
		http.Error(
			w,
			"unknown gallery",
			http.StatusBadRequest,
		)
		return
	}

	q := r.URL.Query()

	rawRel := q.Get("path")

	// Opening the picker starts in the gallery directory.
	// Explicit path="" means the user navigated all the way
	// up to the allowed browser root.
	if !q.Has("path") {
		rawRel = startRel
	}

	_, rel, dir, ok :=
		s.galleryPickerResolve(
			r,
			name,
			rawRel,
		)

	if !ok {
		http.Error(
			w,
			"invalid gallery path",
			http.StatusBadRequest,
		)
		return
	}

	info, err := os.Stat(dir)

	if err != nil || !info.IsDir() {
		http.Error(
			w,
			"folder not found",
			http.StatusNotFound,
		)
		return
	}

	raw, err := os.ReadDir(dir)

	if err != nil {
		http.Error(
			w,
			"could not read folder",
			http.StatusInternalServerError,
		)
		return
	}

	var dirs []galleryBannerPickerEntry
	var files []galleryBannerPickerEntry

	for _, entry := range raw {
		namePart := entry.Name()

		if strings.HasPrefix(namePart, ".") {
			continue
		}

		childRel :=
			pathpkg.Join(rel, namePart)

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.Mode()&os.ModeSymlink != 0 {
			info, err = os.Stat(
				filepath.Join(dir, namePart),
			)

			if err != nil {
				continue
			}
		}

		if info.IsDir() {
			if _, _, _, ok :=
				s.galleryPickerResolve(
					r,
					name,
					childRel,
				); !ok {
				continue
			}

			dirs = append(
				dirs,
				galleryBannerPickerEntry{
					Name:  namePart,
					Rel:   childRel,
					IsDir: true,
				},
			)

			continue
		}

		if !info.Mode().IsRegular() ||
			!bannerPickerImageExt(namePart) {
			continue
		}

		if _, _, _, ok :=
			s.galleryPickerResolve(
				r,
				name,
				childRel,
			); !ok {
			continue
		}

		files = append(
			files,
			galleryBannerPickerEntry{
				Name:  namePart,
				Rel:   childRel,
				IsDir: false,

				PreviewURL: "/internal/gallery/banner-picker/" +
					name +
					"/preview?path=" +
					url.QueryEscape(childRel),
			},
		)
	}

	sort.Slice(
		dirs,
		func(i, j int) bool {
			return strings.ToLower(dirs[i].Name) <
				strings.ToLower(dirs[j].Name)
		},
	)

	sort.Slice(
		files,
		func(i, j int) bool {
			return strings.ToLower(files[i].Name) <
				strings.ToLower(files[j].Name)
		},
	)

	entries := append(dirs, files...)

	truncated := false

	if len(entries) >
		maxGalleryBannerPickerEntries {

		entries =
			entries[:maxGalleryBannerPickerEntries]

		truncated = true
	}

	parent := ""

	if rel != "" {
		parent = pathpkg.Dir(rel)

		if parent == "." {
			parent = ""
		}
	}

	current := "/"

	if rel != "" {
		current = "/" + rel
	}

	s.renderTemplate(
		w,
		"partials/gallery_banner_picker.html",
		galleryBannerPickerData{
			GalleryName: name,
			Current:     current,
			Parent:      parent,
			AtRoot:      rel == "",
			Entries:     entries,
			Truncated:   truncated,
		},
	)
}

func (s *Server) galleryBannerPickerPreviewGet(
	w http.ResponseWriter,
	r *http.Request,
) {
	name := strings.TrimSpace(
		r.PathValue("name"),
	)

	_, _, full, ok :=
		s.galleryPickerResolve(
			r,
			name,
			r.URL.Query().Get("path"),
		)

	if !ok {
		http.NotFound(w, r)
		return
	}

	info, err := os.Stat(full)

	if err != nil ||
		!info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}

	f, err := os.Open(full)

	if err != nil {
		http.NotFound(w, r)
		return
	}

	defer f.Close()

	head := make([]byte, 512)

	n, err := f.Read(head)

	if err != nil &&
		!errors.Is(err, io.EOF) {

		http.NotFound(w, r)
		return
	}

	contentType :=
		http.DetectContentType(head[:n])

	if !galleryBannerTypes[contentType] {
		http.NotFound(w, r)
		return
	}

	if _, err :=
		f.Seek(0, io.SeekStart); err != nil {

		http.NotFound(w, r)
		return
	}

	w.Header().Set(
		"Content-Type",
		contentType,
	)

	w.Header().Set(
		"Cache-Control",
		"private, max-age=60",
	)

	http.ServeContent(
		w,
		r,
		info.Name(),
		info.ModTime(),
		f,
	)
}

func (s *Server) settingsGalleryBannerSelectPost(
	w http.ResponseWriter,
	r *http.Request,
) {
	if !parseFormOK(w, r) {
		return
	}

	name :=
		strings.TrimSpace(
			r.FormValue("name"),
		)

	rel :=
		strings.TrimSpace(
			r.FormValue("path"),
		)

	_, _, sourcePath, ok :=
		s.galleryPickerResolve(
			r,
			name,
			rel,
		)

	if !ok {
		writeInlineFlash(
			w,
			"err",
			"Invalid gallery image.",
		)
		return
	}

	info, err := os.Stat(sourcePath)

	if err != nil ||
		!info.Mode().IsRegular() {

		writeInlineFlash(
			w,
			"err",
			"Selected image no longer exists.",
		)
		return
	}

	if info.Size() <= 0 {
		writeInlineFlash(
			w,
			"err",
			"Selected image is empty.",
		)
		return
	}

	if info.Size() >
		maxGalleryBannerBytes {

		writeInlineFlash(
			w,
			"err",
			"Selected image exceeds the 12 MiB banner limit.",
		)
		return
	}

	source, err :=
		os.Open(sourcePath)

	if err != nil {
		writeInlineFlash(
			w,
			"err",
			"Could not open selected image.",
		)
		return
	}

	defer source.Close()

	head := make([]byte, 512)

	n, err := source.Read(head)

	if err != nil &&
		!errors.Is(err, io.EOF) {

		writeInlineFlash(
			w,
			"err",
			"Could not read selected image.",
		)
		return
	}

	contentType :=
		http.DetectContentType(head[:n])

	if !galleryBannerTypes[contentType] {
		writeInlineFlash(
			w,
			"err",
			"Selected file is not a supported image.",
		)
		return
	}

	if _, err :=
		source.Seek(0, io.SeekStart); err != nil {

		writeInlineFlash(
			w,
			"err",
			"Could not read selected image.",
		)
		return
	}

	bannerPath, ok :=
		s.galleryBannerPath(name)

	if !ok {
		writeInlineFlash(
			w,
			"err",
			"Unknown gallery.",
		)
		return
	}

	if err :=
		os.MkdirAll(
			filepath.Dir(bannerPath),
			0755,
		); err != nil {

		writeInlineFlash(
			w,
			"err",
			"Could not create presentation folder.",
		)
		return
	}

	if err := fsx.WriteDurable(
		bannerPath,
		func(dst *os.File) error {
			_, err :=
				io.Copy(dst, source)

			return err
		},
	); err != nil {

		writeInlineFlash(
			w,
			"err",
			"Could not save selected banner.",
		)
		return
	}

	if err :=
		s.saveGalleryPresentation(
			name,
			defaultGalleryPresentation(),
		); err != nil {

		_ = os.Remove(bannerPath)

		writeInlineFlash(
			w,
			"err",
			"Could not initialize banner position.",
		)
		return
	}

	target :=
		"/settings?edit_banner=" +
			url.QueryEscape(name) +
			"#galleries"

	if isHTMXRequest(r) {
		w.Header().Set(
			"HX-Redirect",
			target,
		)
		w.WriteHeader(http.StatusOK)
		return
	}

	http.Redirect(
		w,
		r,
		target,
		http.StatusSeeOther,
	)
}
