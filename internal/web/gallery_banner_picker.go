package web

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/fsx"
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

// Keep the displayed relative name for navigation, but open the resolved
// relative name through the root. This supports in-scope absolute links.
func galleryPickerResolve(root *os.Root, raw string) (rel, resolved string, err error) {
	rel, ok := cleanGalleryPickerRel(raw)
	if !ok {
		return "", "", fs.ErrInvalid
	}
	if rel == "" {
		return "", ".", nil
	}
	osRel := filepath.FromSlash(rel)
	if !filepath.IsLocal(osRel) {
		return "", "", fs.ErrInvalid
	}
	full, err := filepath.EvalSymlinks(filepath.Join(root.Name(), osRel))
	if err != nil {
		return "", "", err
	}
	resolved, err = filepath.Rel(root.Name(), full)
	if err != nil || !filepath.IsLocal(resolved) {
		return "", "", fs.ErrPermission
	}
	return rel, resolved, nil
}

func (s *Server) openGalleryPickerImage(r *http.Request, name, raw string) (*os.File, os.FileInfo, error) {
	base, _, _, ok := s.galleryPickerScope(r, name)
	if !ok {
		return nil, nil, fs.ErrNotExist
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	_, resolved, err := galleryPickerResolve(root, raw)
	if err != nil {
		return nil, nil, err
	}
	info, err := root.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, fs.ErrInvalid
	}
	f, err := root.Open(resolved)
	if err != nil {
		return nil, nil, err
	}
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fs.ErrInvalid
	}
	return f, info, nil
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

	base, _, startRel, ok :=
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

	root, err := os.OpenRoot(base)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()

	rel, dir, err := galleryPickerResolve(root, rawRel)
	if err != nil {
		http.Error(w, "invalid gallery path", http.StatusBadRequest)
		return
	}
	f, err := root.Open(dir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	raw, err := f.ReadDir(-1)
	_ = f.Close()
	if err != nil {
		http.Error(w, "could not read folder", http.StatusNotFound)
		return
	}

	var dirs []galleryBannerPickerEntry
	var files []galleryBannerPickerEntry

	for _, entry := range raw {
		namePart := entry.Name()

		if strings.HasPrefix(namePart, ".") {
			continue
		}

		childRel := pathpkg.Join(rel, namePart)
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			_, resolved, err := galleryPickerResolve(root, childRel)
			if err != nil {
				continue
			}
			info, err = root.Stat(resolved)
			if err != nil {
				continue
			}
		}

		if info.IsDir() {
			dirs = append(dirs, galleryBannerPickerEntry{Name: namePart, Rel: childRel, IsDir: true})
			continue
		}
		if !info.Mode().IsRegular() || !bannerPickerImageExt(namePart) {
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

	f, info, err := s.openGalleryPickerImage(r, name, r.URL.Query().Get("path"))
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

	source, info, err := s.openGalleryPickerImage(r, name, rel)
	if err != nil {
		writeInlineFlash(w, "err", "Invalid or unavailable gallery image.")
		return
	}
	defer source.Close()

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

	root, dir, err := s.openGalleryPresentation(name)
	if err != nil {
		writeInlineFlash(w, "err", "Could not open gallery presentation.")
		return
	}
	defer root.Close()

	bannerPath := filepath.Join(dir, bannerFilename)
	if err := fsx.WriteDurableIn(root, bannerPath, func(dst *os.File) error {
		n, err := io.Copy(dst, io.LimitReader(source, maxGalleryBannerBytes+1))
		if err == nil && n > maxGalleryBannerBytes {
			return errors.New("banner exceeds size limit")
		}
		return err
	}); err != nil {
		writeInlineFlash(w, "err", "Could not save selected banner.")
		return
	}

	if err :=
		s.saveGalleryPresentation(
			name,
			defaultGalleryPresentation(),
		); err != nil {

		_ = root.Remove(bannerPath)

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
