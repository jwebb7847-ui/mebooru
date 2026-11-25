package webui

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedStaticAssets(t *testing.T) {
	handler := http.StripPrefix("/static/shared/", http.FileServer(http.FS(Static)))
	for _, tc := range []struct {
		name string
		mime string
	}{
		{"base.css", "text/css"},
		{"base.js", "text/javascript"},
		{"htmx.min.js", "text/javascript"},
		{"jetbrains-mono.woff2", "font/woff2"},
		{"jetbrains-mono-bold.woff2", "font/woff2"},
		{"wordmark.woff2", "font/woff2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := fs.ReadFile(Static, tc.name)
			if err != nil || len(body) == 0 {
				t.Fatalf("missing embedded asset: %v", err)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest("GET", "/static/shared/"+tc.name, nil))
			if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), body) ||
				!strings.HasPrefix(w.Header().Get("Content-Type"), tc.mime) {
				t.Fatalf("asset delivery: status=%d MIME=%q", w.Code, w.Header().Get("Content-Type"))
			}
			if strings.HasSuffix(tc.name, ".woff2") && !bytes.HasPrefix(body, []byte("wOF2")) {
				t.Fatal("invalid WOFF2 signature")
			}
		})
	}
	for _, name := range []string{"jetbrains-mono-OFL.txt", "wordmark-OFL.txt"} {
		if b, err := fs.ReadFile(Static, name); err != nil || len(b) == 0 {
			t.Fatalf("missing bundled license %s: %v", name, err)
		}
	}
	b, err := fs.ReadFile(Static, "htmx.min.js")
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(b)) != "d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717" {
		t.Fatal("pinned htmx bytes changed")
	}
}

func TestSharedLoginTemplateAndEscaping(t *testing.T) {
	tmpl, err := template.New("root").Funcs(Funcs()).ParseFS(Templates, "head.html", "login_page.html")
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	data := map[string]any{
		"App": "fixture", "Title": "<script>bad</script>", "BooruName": "mon<fixture>",
		"CSRFToken": "fixture-csrf", "Next": "/gallery?a=1&b=2", "Theme": true,
	}
	if err := tmpl.ExecuteTemplate(&b, "webui/login_page", data); err != nil {
		t.Fatal(err)
	}
	s := b.String()
	for _, want := range []string{"/static/shared/base.css", "/static/main.css", "/theme.css", "fixture-csrf", "/gallery?a=1&amp;b=2", "&lt;script&gt;bad&lt;/script&gt;", "&lt;fixture&gt;"} {
		if !strings.Contains(s, want) {
			t.Errorf("render missing %q", want)
		}
	}
	if strings.Contains(s, "<script>bad</script>") || strings.Contains(s, "<fixture>") {
		t.Fatal("unescaped fixture text")
	}
}

func TestInstalledThemeOverridesBuiltin(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "Default"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Default/theme.css", "Default.css", "Other.css"} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte("/* fixture */"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	themes, _ := ListThemes([]Theme{{Name: "Default", Builtin: true}}, dir)
	if len(themes) != 2 || themes[0].Name != "Default" || themes[0].Builtin ||
		themes[0].Path != filepath.Join(dir, "Default", "theme.css") || themes[1].Name != "Other" {
		t.Fatalf("unexpected theme precedence: %+v", themes)
	}
	if got, ok := FindTheme(themes, "../outside"); ok || got.Name != "Default" {
		t.Fatal("unknown theme did not fall back")
	}
}

func TestSeedThemePreservesEdits(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "themes")
	src := fstest.MapFS{"default/theme.css": {Data: []byte("original")}}
	SeedTheme(src, "default", []string{"theme.css"}, dir, "Default")
	path := filepath.Join(dir, "Default", "theme.css")
	assert := func(want string) {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil || string(b) != want {
			t.Fatalf("theme=%q err=%v; want %q", b, err, want)
		}
	}
	assert("original")
	src["default/theme.css"] = &fstest.MapFile{Data: []byte("updated")}
	SeedTheme(src, "default", []string{"theme.css"}, dir, "Default")
	assert("updated")
	if err := os.WriteFile(path, []byte("user edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	src["default/theme.css"] = &fstest.MapFile{Data: []byte("new release")}
	SeedTheme(src, "default", []string{"theme.css"}, dir, "Default")
	assert("user edit")
}
