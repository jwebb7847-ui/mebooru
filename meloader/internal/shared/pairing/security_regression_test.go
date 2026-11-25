package pairing

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPairingDecisionsAndSingleCollection(t *testing.T) {
	s := NewStore[Request, *Request]()
	offer, refusal := s.Offer(Request{App: "peer", Scopes: []string{"read"}})
	if refusal != nil || offer.Status != Pending || offer.RequestID == "" {
		t.Fatal("offer failed")
	}
	id := offer.RequestID
	var minted atomic.Int32
	mint := func(r Request) (string, error) {
		minted.Add(1)
		if r.App != "peer" || len(r.Scopes) != 1 || r.Scopes[0] != "read" {
			return "", errors.New("unexpected request")
		}
		return "fixture-secret", nil
	}
	if body, f := s.Poll(id, mint); f != nil || body.Status != Pending || body.Token != "" || minted.Load() != 0 {
		t.Fatal("pending request minted a token")
	}
	if !s.SetState(id, Approved) || s.SetState(id, Denied) {
		t.Fatal("decision was not final")
	}
	var collected atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, f := s.Poll(id, mint)
			if f != nil {
				if f.Status != http.StatusNotFound {
					t.Errorf("unexpected poll refusal %v", f.Status)
				}
				return
			}
			if body.Status != Approved {
				t.Errorf("unexpected status %s", body.Status)
			}
			if body.Token != "" {
				if body.Token != "fixture-secret" {
					t.Error("unexpected token")
				}
				collected.Add(1)
			}
		}()
	}
	wg.Wait()
	if minted.Load() != 1 || collected.Load() != 1 {
		t.Fatalf("minted=%d collected=%d", minted.Load(), collected.Load())
	}
	if _, f := s.Poll(id, mint); f == nil || f.Code != CodeNotFound {
		t.Fatal("collected request still present")
	}
	denied, _ := s.Create(Request{App: "denied"})
	if !s.SetState(denied, Denied) {
		t.Fatal("denial failed")
	}
	if body, f := s.Poll(denied, mint); f != nil || body.Status != Denied || body.Token != "" || minted.Load() != 1 {
		t.Fatal("denied request minted token")
	}
	if s.SetState(denied, Approved) {
		t.Fatal("denied request could be approved")
	}
}

func TestPairingMintFailureCanRetry(t *testing.T) {
	s := NewStore[Request, *Request]()
	id, _ := s.Create(Request{App: "peer"})
	s.SetState(id, Approved)
	if _, f := s.Poll(id, func(Request) (string, error) { return "", errors.New("fixture failure") }); f == nil || f.Code != CodeMintFailed {
		t.Fatal("mint failure not reported")
	}
	body, f := s.Poll(id, func(Request) (string, error) { return "fixture", nil })
	if f != nil || body.Token != "fixture" || body.Status != Approved {
		t.Fatal("failed mint could not be retried")
	}
}

func TestPendingReplacementCapAndSweep(t *testing.T) {
	s := NewStore[Request, *Request]()
	first, _ := s.Create(Request{App: "peer"})
	second, ok := s.Create(Request{App: "peer"})
	if !ok || first == second || len(s.ListPending()) != 1 {
		t.Fatal("same-app replacement failed")
	}
	if _, ok := s.Get(first); ok {
		t.Fatal("superseded request still exists")
	}
	for i := 1; i < MaxPending; i++ {
		if _, ok := s.Create(Request{App: fmt.Sprintf("peer-%d", i)}); !ok {
			t.Fatal("cap reached prematurely")
		}
	}
	if _, f := s.Offer(Request{App: "overflow"}); f == nil || f.Status != http.StatusTooManyRequests {
		t.Fatal("pending cap not enforced")
	}
	// Age the fixture directly; no five-minute sleep. Expiry is currently
	// enforced by the store sweep, not independently by Get or Claim.
	s.mu.Lock()
	s.m[second].CreatedAt = time.Now().Add(-pendingTTL - time.Second)
	s.mu.Unlock()
	if len(s.ListPending()) != MaxPending-1 {
		t.Fatal("expired request not swept")
	}
	if _, ok := s.Get(second); ok {
		t.Fatal("swept request still exists")
	}
}

func TestPairingOfferValidation(t *testing.T) {
	for _, app := range []string{"mebooru", "meloader", "Other-App_1"} {
		if !ValidApp(app) {
			t.Errorf("valid name refused: %q", app)
		}
	}
	for _, app := range []string{"", "bad name", "peer (paired)", "a/b", strings.Repeat("x", 33)} {
		if ValidApp(app) {
			t.Errorf("invalid app accepted: %q", app)
		}
	}
	for _, body := range []string{"not json", "{}", "{\"app\":\"\"}", "{\"app\":\""+strings.Repeat("x", maxOfferBody)+"\"}"} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		var dst RequestBody
		if f := DecodeOffer(httptest.NewRecorder(), r, &dst); f == nil || f.Status != http.StatusBadRequest {
			t.Fatal("invalid offer was not refused")
		}
	}
	var dst RequestBody
	r := httptest.NewRequest("POST", "/", strings.NewReader("{\"app\":\"peer\",\"requested_scopes\":[\"read\"]}"))
	if f := DecodeOffer(httptest.NewRecorder(), r, &dst); f != nil || dst.App != "peer" || len(dst.RequestedScopes) != 1 || dst.RequestedScopes[0] != "read" {
		t.Fatal("valid offer not decoded")
	}
}
