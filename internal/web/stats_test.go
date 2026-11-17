package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/jobs"
	"github.com/monbooru/monbooru/internal/models"
)

func TestSettingsActiveGalleryStats(t *testing.T) {
	root := t.TempDir()
	audit, empty := filepath.Join(root, "media"), filepath.Join(root, "empty")
	for _, path := range []string{audit, empty} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(root, "monbooru.toml")
	cfg, err := config.LoadWithDefaults(configPath, func(c *config.Config) {
		c.Galleries = []config.Gallery{{Name: "audit", GalleryPath: audit}, {Name: "empty", GalleryPath: empty}}
		c.DefaultGallery = "audit"
		c.Paths.DataPath = filepath.Join(root, "data")
		c.Paths.ModelPath = filepath.Join(root, "models")
		c.Gallery.WatchEnabled = false
		c.Schedule.Mode = config.ScheduleOff
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(cfg, configPath, jobs.NewManager(), Desktop{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	cx := s.active()
	if _, err := cx.DB.Write.Exec(`INSERT INTO images (id, sha256, canonical_path, file_type, file_size, width, height) VALUES
		(1, 'png',  ?, 'png',  1024, 1920, 1080),
		(2, 'gif',  ?, 'gif',  2048,  600,  400),
		(3, 'mp4',  ?, 'mp4',  4096,  640,  480),
		(4, 'webm', ?, 'webm', 8192,  640,  480),
		(5, 'cbz',  ?, 'cbz', 16384,  NULL, NULL),
		(6, 'jpeg', ?, 'jpeg', 2048,  800,  600)`,
		filepath.Join(audit, "one.png"), filepath.Join(audit, "two.gif"),
		filepath.Join(audit, "three.mp4"), filepath.Join(audit, "four.webm"),
		filepath.Join(audit, "five.cbz"), filepath.Join(audit, "six.jpg")); err != nil {
		t.Fatal(err)
	}
	category := func(name string) int64 {
		t.Helper()
		var id int64
		if err := cx.DB.Read.QueryRow(`SELECT id FROM tag_categories WHERE name = ?`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	tag := func(name string, categoryID int64) int64 {
		t.Helper()
		entry, err := cx.TagSvc.GetOrCreateTag(name, categoryID)
		if err != nil {
			t.Fatal(err)
		}
		return entry.ID
	}
	apply := func(imageID, tagID int64) {
		t.Helper()
		if _, err := cx.TagSvc.AddTagsToOneImage(imageID, []int64{tagID}, "user"); err != nil {
			t.Fatal(err)
		}
	}
	general := category("general")
	motion := tag("motion", general)
	apply(1, motion)
	apply(3, motion)
	apply(2, tag("motion", category("character")))
	tag("unused", general)
	apply(6, tag("missing-only", general))
	if _, err := cx.TagSvc.CreateAlias("moving", general, motion); err != nil {
		t.Fatal(err)
	}
	if _, err := cx.TagSvc.RejectTagName("rejected", general); err != nil {
		t.Fatal(err)
	}
	if marked, err := gallery.SetMissing(cx.DB, 6, true); err != nil || !marked {
		t.Fatalf("mark missing: %t, %v", marked, err)
	}
	for _, id := range []int64{1, 3, 4} {
		if err := gallery.ApplyMetaTags(cx.Handle, id); err != nil {
			t.Fatal(err)
		}
	}
	meta := category("meta")
	highres := tag("highres", meta)
	lore := tag("example_lore", meta)
	if _, err := cx.TagSvc.SyncSourceTags(1, []int64{highres, lore}, "e621", false); err != nil {
		t.Fatal(err)
	}
	cx.InvalidateCaches()

	page := func() string {
		t.Helper()
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, cfg.Server.BaseURL+"/settings", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET /settings: status %d: %s", w.Code, w.Body)
		}
		return w.Body.String()
	}
	body := page()
	for _, want := range []string{
		"Active gallery · audit",
		`<th scope="row">Images</th><td>2</td>`,
		`<th scope="row">Videos</th><td>2</td>`,
		`<th scope="row">Manga archives</th><td>1</td>`,
		`<th scope="row">Indexed media size</th><td title="31744 bytes">31.0 KiB</td>`,
		`<th scope="row">Catalog tags</th><td>12</td>`,
		`<th scope="row">Tags in use</th><td>6</td>`,
		`id="footer-count-tags">12 tags`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page missing %q", want)
		}
	}

	var applied, derived, imported int
	if err := cx.DB.Read.QueryRow(`SELECT
		(SELECT COUNT(*) FROM image_tags it JOIN images i ON i.id = it.image_id WHERE i.is_missing = 0),
		COUNT(DISTINCT s.tag_id) FILTER (WHERE s.source = ?),
		COUNT(DISTINCT s.tag_id) FILTER (WHERE s.source = 'e621')
		FROM image_tag_sources s JOIN tags t ON t.id = s.tag_id WHERE t.category_id = ?`,
		models.TagSourceMonbooru, meta).Scan(&applied, &derived, &imported); err != nil {
		t.Fatal(err)
	}
	if applied != 9 || derived != 3 || imported != 2 {
		t.Errorf("fixture counts: %d assignments, %d derived Meta tags, %d source-backed Meta tags", applied, derived, imported)
	}
	t.Logf("Audit gallery: 12 catalog tags, 6 in use, %d active assignments, %d derived Meta tags, %d source-backed Meta tags", applied, derived, imported)

	if err := s.set.Switch("empty"); err != nil {
		t.Fatal(err)
	}
	body = page()
	for _, want := range []string{
		"Active gallery · empty",
		`<th scope="row">Images</th><td>0</td>`,
		`<th scope="row">Videos</th><td>0</td>`,
		`<th scope="row">Indexed media size</th><td title="0 bytes">0 B</td>`,
		`<th scope="row">Catalog tags</th><td>4</td>`,
		`<th scope="row">Tags in use</th><td>0</td>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("empty gallery settings page missing %q", want)
		}
	}
}
