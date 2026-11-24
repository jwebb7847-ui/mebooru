package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func requestWithCeiling(level string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if level != "" {
		r.AddCookie(&http.Cookie{Name: ratingCeilingCookieName, Value: level})
	}
	return r
}

func TestUnratedBrowserPreference(t *testing.T) {
	restricted := requestWithCeiling("general")
	if !resolveCeiling(restricted, nil).HideUnrated() {
		t.Fatal("restricted ceiling should hide unrated by default")
	}
	all := requestWithCeiling("")
	if resolveCeiling(all, nil).HideUnrated() {
		t.Fatal("default Explicit view should include unrated")
	}

	w := httptest.NewRecorder()
	writeUnratedVisibilityCookie(w, restricted, true)
	visibleCookies := w.Result().Cookies()
	if len(visibleCookies) == 0 {
		t.Fatal("show preference not saved")
	}
	restricted.AddCookie(visibleCookies[0])
	if resolveCeiling(restricted, nil).HideUnrated() {
		t.Fatal("show preference must include unrated under restricted ceiling")
	}
	all.AddCookie(visibleCookies[0])
	if resolveCeiling(all, nil).HideUnrated() {
		t.Fatal("show preference must persist across ceiling changes")
	}

	w = httptest.NewRecorder()
	writeUnratedVisibilityCookie(w, all, false)
	hiddenCookies := w.Result().Cookies()
	if len(hiddenCookies) == 0 || hiddenCookies[0].Value != "hide" {
		t.Fatal("hide preference must persist rather than clear")
	}
	allHidden := requestWithCeiling("")
	allHidden.AddCookie(hiddenCookies[0])
	c := resolveCeiling(allHidden, nil)
	if !c.HideUnrated() || !c.VisibilityActive() {
		t.Fatal("Explicit must be able to hide unrated independently")
	}
	restrictedHidden := requestWithCeiling("questionable")
	restrictedHidden.AddCookie(hiddenCookies[0])
	if !resolveCeiling(restrictedHidden, nil).HideUnrated() {
		t.Fatal("hide preference must persist under lower ceilings")
	}
	invalid := requestWithCeiling("")
	invalid.AddCookie(&http.Cookie{Name: unratedVisibilityCookieName, Value: "invalid"})
	if !resolveCeiling(invalid, nil).HideUnrated() {
		t.Fatal("invalid saved preference should not allow unrated")
	}
}
