package web

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/webui"
	webFS "github.com/monbooru/monloader/web"
)

func TestDefaultBrandingHasNoLegacyMascots(t *testing.T) {
	for _, theme := range builtinThemes {
		if got := themeMascots(theme); len(got) != 0 {
			t.Errorf("theme %s exposes legacy mascot: %v", theme.Name, got)
		}
	}
	// Explicitly installed theme artwork remains supported, even when
	// the operator has overridden the built-in theme of the same name.
	for _, face := range mascotFaces {
		got := themeMascots(webui.Theme{Name: "light", Mascots: map[string]string{face: "/custom/mascot.png"}})
		if got[face] != "/theme.mascot/"+face || len(got) != 1 {
			t.Errorf("custom %s was not preserved: %v", face, got)
		}
	}
}

func TestQueueAndNotFoundDoNotRenderTextFaces(t *testing.T) {
	for _, path := range []string{"templates/partials/queue_rows.html", "templates/notfound.html", "static/main.css"} {
		body, err := fs.ReadFile(webFS.FS, path)
		if err != nil {
			t.Fatal(err)
		}
		for _, face := range []string{"st-kaomoji", "(－ω－)", "(￣～￣;)"} {
			if strings.Contains(string(body), face) {
				t.Errorf("%s contains decorative face %q", path, face)
			}
		}
	}
}
