package gallery

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/monbooru/monbooru/internal/markup"
	"github.com/monbooru/monbooru/internal/models"
)

// SourceInput is the normalized source data accepted by existing
// library services. Shared by ordinary HTTP imports and isolated
// managed metadata candidates; normalization is not authorization.
type SourceInput struct {
	Source string
	PostID string
	URL string
	ParentURL string
	Commentary string
	Translated string
	Original string
	Notes []models.Annotation
	PostFile PostFile
	Withheld Withheld
}
const sourceMaxAnnotations = 500
const sourceMaxAnnotationBodyHTMLLen = 4 * MaxAnnotationBodyLen

func sourceInputMaxLen(field,s string,max int)error{
	if utf8.RuneCountInString(s)>max{return fmt.Errorf("%s must be %d characters or less",field,max)}
	return nil
}
func sourceInputLabel(s string)error{return sourceInputMaxLen("source",s,contract.MaxSourceLabel)}
func sourceInputURL(s string)error{
	if s==""{return nil}
	if utf8.RuneCountInString(s)>contract.MaxURL{return fmt.Errorf("url must be %d characters or less",contract.MaxURL)}
	if !ValidExternalURL(s){return fmt.Errorf("url must start with http:// or https://")}
	return nil
}
func sourcePostFile(w,h int,size int64,ext string)PostFile{
	return PostFile{Width:max(w,0),Height:max(h,0),Size:max(size,0),Ext:strings.TrimSpace(ext)}
}
func sourceFirstRunes(s string,n int)string{
	for i:=range s{if n==0{return s[:i]};n--}
	return s
}

// The limits apply to what is stored, after a DText body has superseded
// the plain one.
func NormalizeSourceInput(p contract.PostFields) (SourceInput, error) {
	if err := sourceInputMaxLen("commentary_dtext", p.CommentaryDText, contract.MaxDText); err != nil {
		return SourceInput{}, err
	}
	if err := sourceInputMaxLen("commentary_translated_dtext", p.CommentaryTranslatedDText, contract.MaxDText); err != nil {
		return SourceInput{}, err
	}
	v := SourceInput{
		Source:     strings.TrimSpace(p.Source),
		PostID:     strings.TrimSpace(p.PostID),
		URL:        strings.TrimSpace(p.URL),
		ParentURL:  strings.TrimSpace(p.ParentURL),
		Commentary: SourceCommentaryFromInput(p.Commentary, p.CommentaryDText),
		Translated: SourceCommentaryFromInput(p.CommentaryTranslated, p.CommentaryTranslatedDText),
		Original:   strings.TrimSpace(p.Original),
		PostFile:   sourcePostFile(p.PostWidth, p.PostHeight, p.PostSize, p.PostExt),
		Withheld:   Withheld{Categories: p.WithheldCategories, Tags: p.WithheldTags},
	}
	v.Notes = SourceAnnotationsFromInput(p.Notes, v.URL)
	if err := sourceInputLabel(v.Source); err != nil {
		return v, err
	}
	if err := sourceInputMaxLen("post_id", v.PostID, contract.MaxPostID); err != nil {
		return v, err
	}
	if err := sourceInputURL(v.URL); err != nil {
		return v, err
	}
	if err := sourceInputURL(v.ParentURL); err != nil {
		return v, fmt.Errorf("parent_url: %v", err)
	}
	if err := sourceInputMaxLen("commentary", v.Commentary, contract.MaxCommentary); err != nil {
		return v, err
	}
	if err := sourceInputMaxLen("commentary_translated", v.Translated, contract.MaxCommentary); err != nil {
		return v, err
	}
	if err := sourceInputMaxLen("original", v.Original, contract.MaxOriginal); err != nil {
		return v, err
	}
	if err := sourceInputMaxLen("post_ext", v.PostFile.Ext, contract.MaxPostExt); err != nil {
		return v, err
	}
	return v, nil
}

func SourceCommentaryFromInput(plain, dtext string) string {
	if strings.TrimSpace(dtext) == "" {
		return strings.TrimSpace(plain)
	}
	body := markup.FromDText(dtext)
	// Converting can grow a body, so the cap applies to what is stored.
	return sourceFirstRunes(body, contract.MaxCommentary)
}

func SourceAnnotationsFromInput(in []contract.NoteBox, postURL string) []models.Annotation {
	if len(in) > sourceMaxAnnotations {
		in = in[:sourceMaxAnnotations]
	}
	var base *url.URL
	if postURL != "" {
		base, _ = url.Parse(postURL)
	}
	out := make([]models.Annotation, 0, len(in))
	for _, n := range in {
		body := n.Body
		if n.BodyHTML != "" {
			body = markup.FromHTML(sourceFirstRunes(n.BodyHTML, sourceMaxAnnotationBodyHTMLLen), base)
		}
		body = sourceFirstRunes(body, MaxAnnotationBodyLen)
		out = append(out, models.Annotation{
			X: max(n.X, 0), Y: max(n.Y, 0), W: max(n.W, 0), H: max(n.H, 0), Body: body,
		})
	}
	return out
}

