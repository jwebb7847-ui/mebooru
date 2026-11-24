// Package bundlepair implements the first-party desktop bootstrap over private
// inherited pipes. It does not listen on a socket or approve HTTP requests.
package bundlepair

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
)

const (
	ready = "mebooru-bundle-v1:ready\n"
	saved = "mebooru-bundle-v1:saved\n"
	commit = "mebooru-bundle-v1:commit\n"
	secretLen = 32
)

// Secrets never belong in command arguments, environment variables or logs.
type Secrets struct {
	Library string // Meloader's bearer credential for Mebooru.
	Loader string  // Mebooru's bearer credential for Meloader.
}

func secret() string {
	b := make([]byte, secretLen/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func valid(s string) bool {
	if len(s) != secretLen {
		return false
	}
	b, err := hex.DecodeString(s)
	return err == nil && hex.EncodeToString(b) == s
}

func write(w io.Writer, s string) error {
	n, err := io.WriteString(w, s)
	if err == nil && n != len(s) {
		err = io.ErrShortWrite
	}
	return err
}

func expect(r io.Reader, frame string) error {
	b := make([]byte, len(frame))
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	if string(b) != frame {
		return errors.New("unexpected bundle bootstrap frame")
	}
	return nil
}

// Parent waits for the actual child to announce ownership of its listener.
// Both sides save through their normal configuration persistence. The caller
// owns the pipes, deadlines, eligibility checks and process lifecycle.
func Parent(r io.Reader, w io.Writer, persist, rollback func(Secrets) error) error {
	if err := expect(r, ready); err != nil {
		return err
	}
	s := Secrets{Library: secret(), Loader: secret()}
	for s.Library == s.Loader {
		s.Loader = secret()
	}
	if err := write(w, s.Library+s.Loader); err != nil {
		return err
	}
	if err := expect(r, saved); err != nil {
		return err
	}
	if err := persist(s); err != nil {
		return err
	}
	if err := write(w, commit); err != nil {
		return errors.Join(err, rollback(s))
	}
	return nil
}

// Child must call this only after it has acquired its configured listener and
// validated its pristine, local configuration. It does not start the queue
// before the parent confirms its own durable commit.
func Child(r io.Reader, w io.Writer, persist, rollback func(Secrets) error) error {
	if err := write(w, ready); err != nil {
		return err
	}
	b := make([]byte, 2*secretLen)
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	s := Secrets{Library: string(b[:secretLen]), Loader: string(b[secretLen:])}
	if !valid(s.Library) || !valid(s.Loader) || s.Library == s.Loader {
		return errors.New("invalid bundle bootstrap credentials")
	}
	if err := persist(s); err != nil {
		return err
	}
	if err := write(w, saved); err != nil {
		return errors.Join(err, rollback(s))
	}
	if err := expect(r, commit); err != nil {
		return errors.Join(err, rollback(s))
	}
	return nil
}
