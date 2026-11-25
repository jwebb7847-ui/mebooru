package web

import (
	"testing"

	"github.com/monbooru/monbooru/internal/shared/webui"
)

func TestDefaultBrandingUsesMebooruMark(t *testing.T) {
	for _, name := range []string{"dark", "light"} {
		got := themeMascots(webui.Theme{Name: name})
		if len(got) != 2 || got["hello"] != "/static/logo.png" || got["sparkle-head"] != "/static/logo.png" {
			t.Errorf("theme %s has unexpected default art: %v", name, got)
		}
	}
	for _, name := range []string{"legacy-dark", "legacy-light", "custom"} {
		if got := themeMascots(webui.Theme{Name: name}); len(got) != 0 {
			t.Errorf("theme %s has unexpected default art: %v", name, got)
		}
	}
	for _, face := range mascotFaces {
		got := themeMascots(webui.Theme{Name: "custom", Mascots: map[string]string{face: "/custom/mascot.png"}})
		if len(got) != 1 || got[face] != "/theme.mascot/"+face {
			t.Errorf("custom %s was not preserved: %v", face, got)
		}
	}
}
