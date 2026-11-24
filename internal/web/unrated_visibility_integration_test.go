package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/jobs"
)

func TestUnratedSidebarAndFooterIntegration(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "media")
	if err := os.MkdirAll(media, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "monbooru.toml")
	cfg, err := config.LoadWithDefaults(configPath, func(c *config.Config) {
		c.Galleries = []config.Gallery{{Name: "QA", GalleryPath: media}}
		c.DefaultGallery = "QA"
		c.Paths.DataPath = filepath.Join(root, "data")
		c.Paths.ModelPath = filepath.Join(root, "models")
		c.Gallery.WatchEnabled = false
		c.Schedule.Mode = config.ScheduleOff
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(cfg, configPath, jobs.NewManager(), Desktop{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	g := server.active()
	if _, err := g.DB.Write.Exec(`INSERT INTO images (id, sha256, canonical_path, file_type, file_size, width, height)
		VALUES (1, 'rated', ?, 'png', 123, 100, 100),
		       (2, 'unrated-2', ?, 'png', 123, 100, 100),
		       (3, 'unrated-3', ?, 'png', 123, 100, 100)`,
		filepath.Join(media, "rated.png"), filepath.Join(media, "unrated-2.png"),
		filepath.Join(media, "unrated-3.png")); err != nil {
		t.Fatal(err)
	}
	var ratingID int64
	if err := g.DB.Read.QueryRow(`SELECT id FROM tag_categories WHERE name = 'rating'`).Scan(&ratingID); err != nil {
		t.Fatal(err)
	}
	general, err := g.TagSvc.GetOrCreateTag("general", ratingID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.TagSvc.AddTagsToOneImage(1, []int64{general.ID}, "user"); err != nil {
		t.Fatal(err)
	}
	const sourceURL = "https://derpibooru.org/images/123"
	if err := gallery.AddSourceMembership(g.DB, 1, "derpibooru", "123", sourceURL); err != nil {
		t.Fatal(err)
	}
	g.InvalidateCaches()
	var rated, unrated int
	if err := g.DB.Read.QueryRow(`SELECT COUNT(*) FROM images WHERE is_missing=0 AND rating_rank >= 0`).Scan(&rated); err != nil {
		t.Fatal(err)
	}
	if err := g.DB.Read.QueryRow(`SELECT COUNT(*) FROM images WHERE is_missing=0 AND rating_rank < 0`).Scan(&unrated); err != nil {
		t.Fatal(err)
	}
	if rated != 1 || unrated != 2 {
		t.Fatalf("rating fixture: rated=%d unrated=%d", rated, unrated)
	}

	page := func(path, rating, visibility string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8455"+path, nil)
		if rating != "" {
			req.AddCookie(&http.Cookie{Name: ratingCeilingCookieName, Value: rating})
		}
		if visibility != "" {
			req.AddCookie(&http.Cookie{Name: unratedVisibilityCookieName, Value: visibility})
		}
		res := httptest.NewRecorder()
		server.Handler().ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("GET %s rating=%s unrated=%s status=%d: %s", path, rating, visibility, res.Code, res.Body)
		}
		return res.Body.String()
	}
	testFooter := func(rating, visibility string, shown, hidden int) {
		t.Helper()
		body := page("/", rating, visibility)
		for _, want := range []string{
			`id="footer-count-images">3 total`,
			`id="footer-shown" title="Images matching the current search and allowed by both visibility controls">` + stringInt(shown) + ` shown`,
			stringInt(hidden) + ` hidden`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("rating=%s unrated=%s: footer missing %q", rating, visibility, want)
			}
		}
	}
	testFooter("", "", 3, 0)
	testFooter("general", "", 1, 2)
	testFooter("general", "show", 3, 0)
	testFooter("", "hide", 1, 2)

	// Detail uses the Images navigation highlight, but unlike galleryData
	// it has no Result. A shared layout must not try to render search
	// counts against detailData. Cover both the gallery click URL and
	// direct detail URL, including a source-backed image.
	for _, tc := range []struct {
		name, path, rating, visibility, source string
	}{
		{"gallery click", "/images/1?back_q=&back_sort=newest&back_order=desc&back_page=1&back_idx=0", "general", "show", sourceURL},
		{"direct unrated detail", "/images/2", "", "show", ""},
		{"rated detail with unrated hidden", "/images/1", "general", "hide", sourceURL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := page(tc.path, tc.rating, tc.visibility)
			for _, want := range []string{`class="detail-media"`, `id="footer-count-images">3 total`, `unrated:`} {
				if !strings.Contains(body, want) {
					t.Errorf("detail missing %q", want)
				}
			}
			for _, forbidden := range []string{`id="footer-shown"`, `id="footer-hidden"`, "template error"} {
				if strings.Contains(body, forbidden) {
					t.Errorf("detail unexpectedly contains %q", forbidden)
				}
			}
			if tc.source != "" && !strings.Contains(body, tc.source) {
				t.Error("detail missing source URL")
			}
		})
	}

	for _, tc := range []struct {
		rating, visibility, tag string
	}{
		{"", "", `href="/?q=tagcount%3Arating%3D0"`},
		{"general", "", `hx-post="/internal/rating-ceiling?unrated=show"`},
	} {
		body := page("/internal/sidebar?ids=1", tc.rating, tc.visibility)
		if !strings.Contains(body, `class="tag-entry tag-entry-virtual"`) ||
			!strings.Contains(body, `title="2 unrated images in this gallery">2</span>`) ||
			!strings.Contains(body, tc.tag) {
			t.Fatalf("unrated sidebar missing: %s", body)
		}
	}
	if body := page("/?q=tagcount%3Arating%3D0", "", "show"); !strings.Contains(body, `>2 shown`) {
		t.Fatalf("unrated search did not show 2 results: %s", body)
	}
}

func stringInt(n int) string { return strconv.Itoa(n) }
