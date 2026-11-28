package acquisitionplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

const (
	maxRuntimeFiles = 2048
	maxRuntimeFileBytes = 512 << 20
	maxRuntimeTreeBytes = 1 << 30
)

// RuntimeManifest is supplied by a trusted installation record, not by
// the worker. This candidate verifies a dedicated, sealed-layout runtime
// directory: every regular file is declared, no extras or symlinks.
// It does not authenticate the record or discover external dependencies.
type RuntimeManifest struct {
	Version string `json:"version"`
	Entry string `json:"entry"`
	Files []RuntimeFile `json:"files"`
}
type RuntimeFile struct {
	Path string `json:"path"`
	SHA256 string `json:"sha256"`
}

func validRuntimePath(p string) bool {
	if len(p) == 0 || len(p) > 512 || p == "." || !fs.ValidPath(p) { return false }
	for _, part := range strings.Split(p, "/") {
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") { return false }
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
				r == '_' || r == '-' || r == '.') { return false }
		}
	}
	_, err := filepath.Localize(p)
	return err == nil
}

// canonicalManifest copies and sorts, so caller order does not affect the
// fingerprint. Reject case-folded collisions and file/directory conflicts.
func canonicalManifest(m RuntimeManifest) (RuntimeManifest, error) {
	if m.Version != StrictQualifiedVersion || len(m.Files) == 0 || len(m.Files) > maxRuntimeFiles {
		return RuntimeManifest{}, ErrMetadataPlan
	}
	entry := "gallery-dl"
	if runtime.GOOS == "windows" { entry += ".exe" }
	if m.Entry != entry { return RuntimeManifest{}, ErrMetadataPlan }
	m.Files = slices.Clone(m.Files)
	slices.SortFunc(m.Files, func(a,b RuntimeFile) int { return strings.Compare(a.Path,b.Path) })
	names := make(map[string]string, len(m.Files))
	dirs := make(map[string]string)
	found := false
	for _, f := range m.Files {
		if !validRuntimePath(f.Path) || !validDigest(f.SHA256) { return RuntimeManifest{}, ErrMetadataPlan }
		key := strings.ToLower(f.Path)
		if _, ok := names[key]; ok { return RuntimeManifest{}, ErrMetadataPlan }
		names[key] = f.Path
		if f.Path == m.Entry { found = true }
		for d := path.Dir(f.Path); d != "."; d = path.Dir(d) {
			k := strings.ToLower(d)
			if old, ok := dirs[k]; ok && old != d { return RuntimeManifest{}, ErrMetadataPlan }
			dirs[k] = d
		}
	}
	for d := range dirs { if _, ok := names[d]; ok { return RuntimeManifest{}, ErrMetadataPlan } }
	if !found { return RuntimeManifest{}, ErrMetadataPlan }
	return m, nil
}

func RuntimeManifestDigest(m RuntimeManifest) (string, error) {
	m, err := canonicalManifest(m)
	if err != nil { return "", err }
	body, err := json.Marshal(m)
	if err != nil { return "", ErrMetadataPlan }
	digest := sha256.Sum256(append([]byte("runtime-manifest-v1\x00"),body...))
	return hex.EncodeToString(digest[:]), nil
}

// VerifyRuntimeTree checks actual file bytes against independently trusted
// manifest and identity. Root must be a dedicated absolute directory,
// not the app/profile directory. No execution, PATH lookup, or discovery.
// A nil result is a point-in-time filesystem check only. It cannot prove
// version, interpreter/system dependencies, record provenance, or protect
// against same-user replacement between verification and launch.
func VerifyRuntimeTree(ctx context.Context, root string, m RuntimeManifest, expected RuntimeIdentity) error {
	if ctx.Err() != nil || !expected.valid() || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return ErrMetadataPlan
	}
	m, err := canonicalManifest(m)
	if err != nil { return err }
	digest, err := RuntimeManifestDigest(m)
	if err != nil || digest != expected.ManifestSHA256 || m.Version != expected.Version {
		return ErrMetadataPlan
	}
	files := make(map[string]string,len(m.Files))
	dirs := map[string]bool{".":true}
	for _, f := range m.Files {
		files[f.Path] = f.SHA256
		for d:=path.Dir(f.Path); d!="."; d=path.Dir(d){ dirs[d]=true }
	}
	if files[m.Entry] != expected.ArtifactSHA256 { return ErrMetadataPlan }
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root { return ErrMetadataPlan }
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() { return ErrMetadataPlan }
	seen := make(map[string]bool,len(files))
	var total int64
	err = filepath.WalkDir(root,func(p string,d fs.DirEntry,walkErr error) error {
		if walkErr != nil || ctx.Err()!=nil || d==nil { return ErrMetadataPlan }
		rel, err := filepath.Rel(root,p)
		if err != nil { return ErrMetadataPlan }
		rel = filepath.ToSlash(rel)
		i, err := d.Info()
		if err != nil { return ErrMetadataPlan }
		if i.IsDir() {
			if !dirs[rel] { return ErrMetadataPlan }
			return nil
		}
		want, ok := files[rel]
		if !ok || seen[rel] || !i.Mode().IsRegular() || i.Size() < 0 ||
			i.Size() > maxRuntimeFileBytes || i.Size() > maxRuntimeTreeBytes-total ||
			(rel==m.Entry && (i.Size()==0 || runtime.GOOS!="windows" && i.Mode().Perm()&0111==0)) {
			return ErrMetadataPlan
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil || real != p { return ErrMetadataPlan }
		f, err := os.Open(p)
		if err != nil { return ErrMetadataPlan }
		defer f.Close()
		opened, err := f.Stat()
		if err != nil || !opened.Mode().IsRegular() || !os.SameFile(i,opened) || i.Size()!=opened.Size() {
			return ErrMetadataPlan
		}
		h := sha256.New()
		var n int64
		buf := make([]byte,64<<10)
		for {
			if ctx.Err()!=nil { return ErrMetadataPlan }
			nr, readErr := f.Read(buf)
			n += int64(nr)
			if n>i.Size() { return ErrMetadataPlan }
			_,_=h.Write(buf[:nr])
			if readErr==io.EOF { break }
			if readErr!=nil || nr==0 { return ErrMetadataPlan }
		}
		after,err:=os.Lstat(p)
		if err!=nil || !os.SameFile(i,after) || after.Size()!=i.Size() ||
			after.ModTime()!=i.ModTime() || n!=i.Size() || hex.EncodeToString(h.Sum(nil))!=want {
			return ErrMetadataPlan
		}
		total+=n
		seen[rel]=true
		return nil
	})
	if err!=nil || ctx.Err()!=nil || len(seen)!=len(files) { return ErrMetadataPlan }
	return nil
}
