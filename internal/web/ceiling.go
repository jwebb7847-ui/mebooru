package web

import (
	"net/http"

	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/monbooru/monbooru/internal/library"
	"github.com/monbooru/monbooru/internal/shared/session"
	"github.com/monbooru/monbooru/internal/tags"
)

const ratingCeilingCookieName = "monbooru_rating_ceiling"
const unratedVisibilityCookieName = "monbooru_unrated_visibility"

// Without a preference, Explicit shows everything and lower ceilings hide
// unrated media. After a user choice, this becomes an independent toggle.
func showUnrated(r *http.Request) bool {
	switch session.FromCookie(r, unratedVisibilityCookieName) {
	case "show":
		return true
	case "hide":
		return false
	case "":
		return readRatingCookie(r) == ""
	default:
		return false
	}
}

func writeUnratedVisibilityCookie(w http.ResponseWriter, r *http.Request, show bool) {
	if show {
		setPrefCookie(w, r, unratedVisibilityCookieName, "show")
		return
	}
	setPrefCookie(w, r, unratedVisibilityCookieName, "hide")
}

func resolveCeiling(r *http.Request, cx *library.Gallery) *Ceiling {
	return library.NewCeiling(readRatingCookie(r), cx, !showUnrated(r))
}

// The value reaches the search AST: anything outside the closed set is dropped.
func readRatingCookie(r *http.Request) string {
	if v := session.FromCookie(r, ratingCeilingCookieName); tags.IsCanonicalRating(v) {
		return v
	}
	return ""
}

// The top level is no ceiling at all, so it clears the cookie.
func writeRatingCookie(w http.ResponseWriter, r *http.Request, level string) {
	if rank := contract.RatingRank(level); rank >= 0 && rank < len(contract.RatingLevels)-1 {
		setPrefCookie(w, r, ratingCeilingCookieName, level)
		return
	}
	setPrefCookie(w, r, ratingCeilingCookieName, "")
}
