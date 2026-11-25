package apitoken

import (
	"encoding/hex"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestTokenAuthorizationAndRevocation(t *testing.T) {
	read, secret := New("Reader", []string{"read"})
	if len(secret) != 32 || len(read.ID) != 16 || read.TokenHash != Hash(secret) || read.TokenHash == secret {
		t.Fatal("unexpected token material")
	}
	if _, err := hex.DecodeString(secret); err != nil {
		t.Fatal("secret is not hexadecimal")
	}
	if _, err := time.Parse(time.RFC3339, read.CreatedAt); err != nil {
		t.Fatal(err)
	}
	write := FromSecret("Writer", "fixture-write", []string{"read", "write"})
	l := List{read, write}
	for _, tc := range []struct {
		name, header, scope string
		status int
		code string
	}{
		{"read", "Bearer "+secret, "read", 0, ""},
		{"write refused", "Bearer "+secret, "write", http.StatusForbidden, "insufficient_scope"},
		{"delete refused", "Bearer fixture-write", "delete", http.StatusForbidden, "insufficient_scope"},
		{"write allowed", "Bearer fixture-write", "write", 0, ""},
		{"missing", "", "read", http.StatusUnauthorized, "unauthorized"},
		{"wrong scheme", "Basic "+secret, "read", http.StatusUnauthorized, "unauthorized"},
		{"wrong secret", "Bearer wrong", "read", http.StatusUnauthorized, "unauthorized"},
		{"stored hash is not bearer", "Bearer "+read.TokenHash, "read", http.StatusUnauthorized, "unauthorized"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, status, code, _ := l.Authorize(tc.header, tc.scope)
			if status != tc.status || code != tc.code || (got != nil) != (status == 0) {
				t.Fatalf("status=%d code=%q granted=%v", status, code, got != nil)
			}
		})
	}
	if !l.SetScopes(write.ID, []string{"read"}) {
		t.Fatal("scope update failed")
	}
	if _, status, _, _ := l.Authorize("Bearer fixture-write", "write"); status != http.StatusForbidden {
		t.Fatal("removed scope still granted")
	}
	if !l.Remove(read.ID) || l.Remove(read.ID) {
		t.Fatal("unexpected revocation result")
	}
	if _, status, _, _ := l.Authorize("Bearer "+secret, "read"); status != http.StatusUnauthorized {
		t.Fatal("revoked bearer still works")
	}
	if !l.Remove(write.ID) {
		t.Fatal("last token removal failed")
	}
	if _, status, code, _ := l.Authorize("Bearer fixture-write", "read"); status != http.StatusServiceUnavailable || code != "api_disabled" {
		t.Fatal("empty token list did not disable API")
	}
}

func TestTokenNamesAndScopeFiltering(t *testing.T) {
	for _, name := range []string{"", "  ", "Peer (paired)", "Peer (PAIRED) "} {
		if ValidateName(name) == nil {
			t.Errorf("accepted reserved or empty name %q", name)
		}
	}
	if err := ValidateName("My reader"); err != nil {
		t.Fatal(err)
	}
	token := FromSecret("Reader", "fixture", []string{"read"})
	l := List{token}
	if err := l.Add(FromSecret("reader", "other", nil)); err == nil {
		t.Fatal("case-insensitive duplicate accepted")
	}
	if !l.NameExists(" READER ") {
		t.Fatal("normalized name lookup failed")
	}
	got := Filter([]string{"delete", "read", "read", "unknown"}, []string{"read", "write"})
	if !reflect.DeepEqual(got, []string{"read"}) {
		t.Fatalf("unexpected scopes %v", got)
	}
}
