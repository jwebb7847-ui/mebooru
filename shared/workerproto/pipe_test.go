package workerproto

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

// This helper is a disposable subprocess of the test executable, not the
// application or a production worker. Stdout is exclusively the protocol.
func TestPipeFixtureChild(t *testing.T) {
	mode := os.Getenv("MEBOORU_WORKER_PIPE_FIXTURE")
	switch mode {
	case "exit", "stall":
		if err := Handshake(os.Stdin, os.Stdout, Worker); err != nil {
			os.Exit(2)
		}
		if mode == "exit" {
			os.Exit(7)
		}
		if err := WriteEnvelope(os.Stdout, Envelope{Kind: Event, Op: Operation("fixture.waiting")}); err != nil {
			os.Exit(3)
		}
		// Block until the parent closes the pipe or terminates this process.
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "1":
		// Normal bidirectional exchange below.
	default:
		return
	}
	if err := fixtureChild(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "pipe fixture:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func fixtureChild(r io.Reader, w io.Writer) error {
	if err := Handshake(r, w, Worker); err != nil {
		return err
	}
	first, err := ReadEnvelope(r)
	if err != nil {
		return err
	}
	second, err := ReadEnvelope(r)
	if err != nil {
		return err
	}
	if first.Kind != Request || second.Kind != Request || first.ID == second.ID ||
		first.Op != Health || second.Op != QueueList ||
		!AllowedDirection(Library, first.Op) || !AllowedDirection(Library, second.Op) {
		return fmt.Errorf("unexpected fixture requests")
	}
	// Reuse ID 1 in the opposite direction. IDs are endpoint-scoped.
	if err := WriteEnvelope(w, Envelope{Kind: Request, ID: 1, Op: GalleryList}); err != nil {
		return err
	}
	callback, err := ReadEnvelope(r)
	if err != nil || callback.Kind != Response || callback.ID != 1 || callback.Error != nil {
		return fmt.Errorf("callback failed")
	}
	// Reverse completion order: requests need correlation, not FIFO answers.
	if err := WriteEnvelope(w, Envelope{Kind: Response, ID: second.ID, Payload: json.RawMessage("{}")}); err != nil {
		return err
	}
	if err := WriteEnvelope(w, Envelope{Kind: Event, Op: Operation("worker.progress"), Payload: json.RawMessage("{}")}); err != nil {
		return err
	}
	if err := WriteEnvelope(w, Envelope{Kind: Response, ID: first.ID, Payload: json.RawMessage("{}")}); err != nil {
		return err
	}
	pending, err := ReadEnvelope(r)
	if err != nil || pending.Kind != Request || pending.Op != QueueGet {
		return fmt.Errorf("expected cancelable request")
	}
	cancel, err := ReadEnvelope(r)
	if err != nil || cancel.Kind != Cancel || cancel.ID != pending.ID {
		return fmt.Errorf("expected correlated cancel")
	}
	if err := WriteEnvelope(w, Envelope{Kind: Response, ID: pending.ID, Error: &Fault{Code: "canceled"}}); err != nil {
		return err
	}
	if _, err := ReadEnvelope(r); err != io.EOF {
		return fmt.Errorf("expected parent EOF: %v", err)
	}
	return nil
}

func TestInheritedPipeSubprocess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestPipeFixtureChild$")
	cmd.Env = append(os.Environ(), "MEBOORU_WORKER_PIPE_FIXTURE=1")
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	toChild, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	fromChild, err := cmd.StdoutPipe()
	if err != nil {
		_ = toChild.Close()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		_ = toChild.Close()
		_ = fromChild.Close()
		t.Fatal(err)
	}
	waited := false
	defer func() {
		_ = toChild.Close()
		_ = fromChild.Close()
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	send := func(e Envelope) {
		t.Helper()
		if err := WriteEnvelope(toChild, e); err != nil {
			t.Fatal(err)
		}
	}
	receive := func() Envelope {
		t.Helper()
		e, err := ReadEnvelope(fromChild)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	if err := Handshake(fromChild, toChild, Library); err != nil {
		t.Fatal(err)
	}
	send(Envelope{Kind: Request, ID: 1, Op: Health})
	send(Envelope{Kind: Request, ID: 2, Op: QueueList})
	if e := receive(); e.Kind != Request || e.ID != 1 || e.Op != GalleryList || !AllowedDirection(Worker, e.Op) {
		t.Fatalf("unexpected callback: %+v", e)
	}
	send(Envelope{Kind: Response, ID: 1, Payload: json.RawMessage("{}")})
	if e := receive(); e.Kind != Response || e.ID != 2 || e.Error != nil {
		t.Fatalf("unexpected second response: %+v", e)
	}
	if e := receive(); e.Kind != Event || e.ID != 0 || e.Op != Operation("worker.progress") {
		t.Fatalf("unexpected event: %+v", e)
	}
	if e := receive(); e.Kind != Response || e.ID != 1 || e.Error != nil {
		t.Fatalf("unexpected first response: %+v", e)
	}
	send(Envelope{Kind: Request, ID: 3, Op: QueueGet})
	send(Envelope{Kind: Cancel, ID: 3})
	if e := receive(); e.Kind != Response || e.ID != 3 || e.Error == nil || e.Error.Code != "canceled" {
		t.Fatalf("unexpected cancellation: %+v", e)
	}
	if err := toChild.Close(); err != nil {
		t.Fatal(err)
	}
	// Drain stdout through EOF before Wait closes the pipe.
	if _, err := ReadEnvelope(fromChild); err != io.EOF {
		t.Fatalf("expected child EOF: %v", err)
	}
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatalf("fixture process failed: %v: %s", err, diagnostics.String())
	}
}

func TestInheritedPipePeerLossAndCancellation(t *testing.T) {
	for _, mode := range []string{"exit", "stall"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestPipeFixtureChild$")
			cmd.Env = append(os.Environ(), "MEBOORU_WORKER_PIPE_FIXTURE="+mode)
			toChild, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			fromChild, err := cmd.StdoutPipe()
			if err != nil {
				_ = toChild.Close()
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				_ = toChild.Close()
				_ = fromChild.Close()
				t.Fatal(err)
			}
			waited := false
			defer func() {
				_ = toChild.Close()
				_ = fromChild.Close()
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			if err := Handshake(fromChild, toChild, Library); err != nil {
				t.Fatal(err)
			}
			if mode == "stall" {
				e, err := ReadEnvelope(fromChild)
				if err != nil || e.Kind != Event || e.Op != Operation("fixture.waiting") {
					t.Fatalf("expected waiting fixture: %+v %v", e, err)
				}
				// The process is known to be waiting on the pipe. Cancellation
				// must unblock reads through child termination.
				cancel()
			}
			if _, err := ReadEnvelope(fromChild); err != io.EOF {
				t.Fatalf("expected peer EOF, got %v", err)
			}
			err = cmd.Wait()
			waited = true
			if err == nil {
				t.Fatal("unexpected successful exit")
			}
			if mode == "exit" {
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 7 {
					t.Fatalf("unexpected peer exit: %v", err)
				}
			}
		})
	}
}
