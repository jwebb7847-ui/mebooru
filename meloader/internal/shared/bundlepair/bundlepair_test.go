package bundlepair

import (
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestBootstrapCommitAndRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false:"success", true:"parent persistence failure"}[fail], func(t *testing.T) {
			parent, child := net.Pipe()
			defer parent.Close()
			defer child.Close()
			deadline := time.Now().Add(2*time.Second)
			_ = parent.SetDeadline(deadline)
			_ = child.SetDeadline(deadline)
			var parentSaved, childSaved Secrets
			var childRolledBack bool
			childResult := make(chan error, 1)
			go func() {
				defer child.Close()
				childResult <- Child(child, child, func(s Secrets) error {
					childSaved = s
					return nil
				}, func(s Secrets) error {
					childRolledBack = s == childSaved
					return nil
				})
			}()
			err := Parent(parent, parent, func(s Secrets) error {
				if fail {
					return errors.New("simulated save failure")
				}
				parentSaved = s
				return nil
			}, func(s Secrets) error {
				return errors.New("unexpected parent rollback")
			})
			_ = parent.Close()
			childErr := <-childResult
			if fail {
				if err == nil || childErr == nil || !childRolledBack {
					t.Fatalf("failed parent did not undo child save")
				}
				return
			}
			if err != nil || childErr != nil || childRolledBack ||
				parentSaved != childSaved || !valid(parentSaved.Library) ||
				!valid(parentSaved.Loader) || parentSaved.Library == parentSaved.Loader {
				t.Fatal("bootstrap did not commit matching distinct credentials")
			}
		})
	}
}

func TestUnexpectedReadyDoesNotGrant(t *testing.T) {
	if err := Parent(strings.NewReader(strings.Repeat("x", len(ready))), io.Discard,
		func(Secrets) error { t.Fatal("unexpected grant"); return nil },
		func(Secrets) error { t.Fatal("unexpected rollback"); return nil }); err == nil {
		t.Fatal("unexpected handshake accepted")
	}
}

func TestInvalidCredentialsDoNotPersist(t *testing.T) {
	for _, input := range []string{strings.Repeat("z", 64), strings.Repeat("a", 64), "truncated"} {
		if err := Child(strings.NewReader(input), io.Discard,
			func(Secrets) error { t.Fatal("unexpected grant"); return nil },
			func(Secrets) error { t.Fatal("unexpected rollback"); return nil }); err == nil {
			t.Fatal("invalid credentials accepted")
		}
	}
}
