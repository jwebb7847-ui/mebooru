package web

import (
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestUnratedBrowserPreference(t *testing.T) {
    r:=httptest.NewRequest(http.MethodGet,"/",nil)
    r.AddCookie(&http.Cookie{Name:ratingCeilingCookieName,Value:"general"})
    if !resolveCeiling(r,nil).HideUnrated() {
        t.Fatal("restricted rating must hide unrated by default")
    }
    w:=httptest.NewRecorder()
    writeUnratedVisibilityCookie(w,r,true)
    cookies:=w.Result().Cookies()
    if len(cookies)==0 {t.Fatal("show preference cookie not written")}
    r.AddCookie(cookies[0])
    if resolveCeiling(r,nil).HideUnrated() {
        t.Fatal("show preference must include unrated media")
    }
    r2:=httptest.NewRequest(http.MethodGet,"/",nil)
    r2.AddCookie(&http.Cookie{Name:ratingCeilingCookieName,Value:"general"})
    r2.AddCookie(&http.Cookie{Name:unratedVisibilityCookieName,Value:"invalid"})
    if !resolveCeiling(r2,nil).HideUnrated() {
        t.Fatal("invalid preference must fail closed")
    }
    r3:=httptest.NewRequest(http.MethodGet,"/",nil)
    if resolveCeiling(r3,nil).HideUnrated() {
        t.Fatal("explicit/full visibility must show all media")
    }
}
