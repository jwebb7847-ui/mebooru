package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/library"
	"github.com/monbooru/monbooru/internal/models"
)

const (
	maxAnnotationBodyLen = gallery.MaxAnnotationBodyLen
	maxAnnotations       = 500
)

func validateMaxLen(field, s string, max int) error {
	if utf8.RuneCountInString(s) > max {
		return fmt.Errorf("%s must be %d characters or less", field, max)
	}
	return nil
}

func validateImageSource(s string) error { return validateMaxLen("source", s, contract.MaxSourceLabel) }

func validateImageURL(s string) error {
	if s == "" {
		return nil
	}
	if utf8.RuneCountInString(s) > contract.MaxURL {
		return fmt.Errorf("url must be %d characters or less", contract.MaxURL)
	}
	if !gallery.ValidExternalURL(s) {
		return fmt.Errorf("url must start with http:// or https://")
	}
	return nil
}

func validateImageCollection(s string) error {
	return validateMaxLen("collection", s, contract.MaxCollection)
}

type provenanceJSON struct {
	contract.PostFields
}

func provenanceFromForm(r *http.Request) provenanceJSON {
	p := provenanceJSON{contract.PostFields{
		Source:                    r.FormValue(contract.FieldSource),
		PostID:                    r.FormValue(contract.FieldPostID),
		URL:                       r.FormValue(contract.FieldURL),
		ParentURL:                 r.FormValue(contract.FieldParentURL),
		Commentary:                r.FormValue(contract.FieldCommentary),
		CommentaryTranslated:      r.FormValue(contract.FieldCommentaryTranslated),
		CommentaryDText:           r.FormValue(contract.FieldCommentaryDText),
		CommentaryTranslatedDText: r.FormValue(contract.FieldCommentaryTranslatedDText),
		Original:                  r.FormValue(contract.FieldOriginal),
		PostWidth:                 atoiOrZero(r.FormValue(contract.FieldPostWidth)),
		PostHeight:                atoiOrZero(r.FormValue(contract.FieldPostHeight)),
		PostSize:                  int64(atoiOrZero(r.FormValue(contract.FieldPostSize))),
		PostExt:                   r.FormValue(contract.FieldPostExt),
	}}
	if raw := r.FormValue(contract.FieldNotes); strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &p.Notes); err != nil {
			p.Notes = nil
		}
	}
	for field, list := range map[string]*[]string{
		contract.FieldWithheldCategories: &p.WithheldCategories,
		contract.FieldWithheldTags:       &p.WithheldTags,
	} {
		if raw := r.FormValue(field); raw != "" {
			if err := json.Unmarshal([]byte(raw), list); err != nil {
				*list = nil
			}
		}
	}
	return p
}

type provenance struct {
	source     string
	postID     string
	url        string
	parentURL  string
	commentary string
	translated string
	original   string
	notes      []models.Annotation
	postFile   gallery.PostFile
	withheld   gallery.Withheld
}

// Use the same source normalization as managed library acquisition.
func (p provenanceJSON) read() (provenance, error) {
	v,err:=gallery.NormalizeSourceInput(p.PostFields)
	return provenance{source:v.Source,postID:v.PostID,url:v.URL,parentURL:v.ParentURL,
		commentary:v.Commentary,translated:v.Translated,original:v.Original,
		notes:v.Notes,postFile:v.PostFile,withheld:v.Withheld},err
}

func validateCollection(collection string, order *int) error {
	if err := validateImageCollection(collection); err != nil {
		return err
	}
	if order != nil {
		if *order < 1 {
			return fmt.Errorf("collection_order must be 1 or higher")
		}
		if collection == "" {
			return fmt.Errorf("collection_order requires a non-empty collection")
		}
	}
	return nil
}

type imageResponse struct {
	ID                 int64                 `json:"id"`
	SHA256             string                `json:"sha256"`
	MD5                string                `json:"md5"`
	CanonicalPath      string                `json:"canonical_path"`
	Aliases            []string              `json:"aliases"`
	FileType           string                `json:"file_type"`
	Width              *int                  `json:"width"`
	Height             *int                  `json:"height"`
	FileSize           int64                 `json:"file_size"`
	IsFavorited        bool                  `json:"is_favorited"`
	IsInbox            bool                  `json:"is_inbox"`
	IsMissing          bool                  `json:"is_missing"`
	AutoTaggedAt       *time.Time            `json:"auto_tagged_at"`
	SourceType         string                `json:"source_type"`
	Origin             string                `json:"origin"`
	Source             string                `json:"source"`
	URL                string                `json:"url"`
	Note               string                `json:"note"`
	OriginalSource     string                `json:"original_source"`
	PageCount          *int                  `json:"page_count"`
	Series             string                `json:"collection"`
	SeriesOrder        *int                  `json:"collection_order"`
	Collections        []collectionJSON      `json:"collections,omitempty"`
	Sources            []sourceJSON          `json:"sources,omitempty"`
	Annotations        []annotationJSON      `json:"annotations,omitempty"`
	Phash              *string               `json:"phash"`
	IngestedAt         time.Time             `json:"ingested_at"`
	ThumbnailURL       string                `json:"thumbnail_url"`
	Tags               []imageTagJSON        `json:"tags"`
	TagSources         map[string][]string   `json:"tag_sources,omitempty"`
	ScheduledLookup    *bool                 `json:"scheduled_lookup,omitempty"`
	ScheduledLookupPTR *bool                 `json:"scheduled_lookup_ptr,omitempty"`
	Lookup             map[string]lookupJSON `json:"lookup,omitempty"`
}

type lookupJSON struct {
	LastAt     string `json:"last_at,omitempty"`
	LastResult string `json:"last_result,omitempty"`
	Attempts   int    `json:"attempts"`
	NextDueAt  string `json:"next_due_at,omitempty"`
}

type collectionJSON struct {
	Name  string `json:"name"`
	Order *int   `json:"order"`
}

type sourceJSON struct {
	Site                 string  `json:"site"`
	PostID               string  `json:"post_id,omitempty"`
	URL                  string  `json:"url"`
	Commentary           string  `json:"commentary,omitempty"`
	CommentaryTranslated string  `json:"commentary_translated,omitempty"`
	Original             string  `json:"original,omitempty"`
	Similarity           float64 `json:"similarity,omitempty"`
}

type annotationJSON struct {
	Site     string `json:"site,omitempty"`
	PostID   string `json:"post_id,omitempty"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
	W        int    `json:"w"`
	H        int    `json:"h"`
	Body     string `json:"body"`
	BodyText string `json:"body_text,omitempty"`
}

const maxAnnotationBodyHTMLLen = 4 * maxAnnotationBodyLen

const maxFetchMessageLen = 1000

func firstRunes(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}

// DText wins over the plain rendering: only a booru's own markup still
// says what the artist wrote.
func commentaryFromInput(plain, dtext string) string {
	return gallery.SourceCommentaryFromInput(plain,dtext)
}
func annotationsFromInput(in []contract.NoteBox, postURL string) []models.Annotation {
	return gallery.SourceAnnotationsFromInput(in,postURL)
}

type imageTagJSON struct {
	Name       string   `json:"name"`
	Category   string   `json:"category"`
	IsAuto     bool     `json:"is_auto"`
	Confidence *float64 `json:"confidence"`
	TaggerName *string  `json:"tagger_name"`
}

func makeImageResponse(g *library.Gallery, img models.Image, tags []imageTagJSON, aliases []string) imageResponse {
	if tags == nil {
		tags = []imageTagJSON{}
	}
	if aliases == nil {
		aliases = []string{}
	}
	return imageResponse{
		ID:             img.ID,
		SHA256:         img.SHA256,
		MD5:            img.MD5,
		CanonicalPath:  img.CanonicalPath,
		Aliases:        aliases,
		FileType:       img.FileType,
		Width:          img.Width,
		Height:         img.Height,
		FileSize:       img.FileSize,
		IsFavorited:    img.IsFavorited,
		IsInbox:        img.IsInbox,
		IsMissing:      img.IsMissing,
		AutoTaggedAt:   img.AutoTaggedAt,
		SourceType:     img.SourceType,
		Origin:         img.Origin,
		Source:         img.Source,
		URL:            img.URL,
		Note:           img.Note,
		OriginalSource: img.OriginalSource,
		PageCount:      img.PageCount,
		Series:         img.Series,
		SeriesOrder:    img.SeriesOrder,
		Phash:          phashHexPtr(img.Phash),
		IngestedAt:     img.IngestedAt,
		ThumbnailURL:   "/thumbnails/" + g.Name + "/" + strconv.FormatInt(img.ID, 10) + ".jpg",
		Tags:           tags,
	}
}

func phashHexPtr(p *int64) *string {
	if p == nil {
		return nil
	}
	s := fmt.Sprintf("%016x", uint64(*p))
	return &s
}
