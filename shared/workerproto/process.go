package workerproto

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

var ErrStartupTimeout = errors.New("private worker startup timed out")
var ErrForcedStop = errors.New("private worker required forced termination")
var ErrShutdownRefused = errors.New("private worker refused shutdown")

// ProcessOptions is internal supervisor configuration, never browser or
// protocol input. The caller must verify the bundled executable's identity
// and profile ownership before launch. No PATH lookup or shell is used.
type ProcessOptions struct {
	Executable string
	Args []string
	Env []string
	Dir string
	Stderr *os.File
	StartupTimeout time.Duration
	StopTimeout time.Duration
}

// OwnedProcess is an isolated supervision primitive. It owns exactly one
// direct child, not a process tree. Production descendant containment,
// singleton/profile locking and restart policy must be implemented separately.
type OwnedProcess struct {
	Session *Session
	cmd *exec.Cmd
	cancel context.CancelCauseFunc
	done chan struct{}
	exitErr error
	stopTimeout time.Duration
	stopOnce sync.Once
	stopErr error
}

func (p *OwnedProcess) Done() <-chan struct{} { return p.done }

// Wait reports the direct child's exit result. It never calls cmd.Wait
// twice; all observers share the result published before done closes.
func (p *OwnedProcess) Wait(ctx context.Context) error {
	select {
	case <-p.done:
		return p.exitErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// StartOwnedProcess creates two inherited channels and closes the parent's
// copies of the child ends. Explicit files rather than Cmd.StdoutPipe let
// Wait run independently of the protocol reader. No listener is opened.
func StartOwnedProcess(ctx context.Context, opts ProcessOptions, handler Handler) (*OwnedProcess, error) {
	if !filepath.IsAbs(opts.Executable) {
		return nil, errors.New("worker executable must be an absolute path")
	}
	if err := ctx.Err(); err != nil { return nil, err }
	if opts.StartupTimeout <= 0 { opts.StartupTimeout = 10*time.Second }
	if opts.StopTimeout <= 0 { opts.StopTimeout = 3*time.Second }
	childRead, parentWrite, err := os.Pipe()
	if err != nil { return nil, err }
	parentRead, childWrite, err := os.Pipe()
	if err != nil {
		_ = childRead.Close()
		_ = parentWrite.Close()
		return nil, err
	}
	defer childRead.Close()
	defer childWrite.Close()
	lifetime, cancel := context.WithCancelCause(ctx)
	// CommandContext also terminates a child that holds the pipe open
	// during startup cancellation, independently of pipe Close semantics.
	cmd := exec.CommandContext(lifetime, opts.Executable, opts.Args...)
	cmd.Dir = opts.Dir
	cmd.Env = opts.Env
	cmd.Stdin = childRead
	cmd.Stdout = childWrite
	if opts.Stderr != nil { cmd.Stderr = opts.Stderr }
	if err := cmd.Start(); err != nil {
		cancel(err)
		_ = parentRead.Close()
		_ = parentWrite.Close()
		return nil, err
	}
	_ = childRead.Close()
	_ = childWrite.Close()
	p := &OwnedProcess{cmd:cmd,cancel:cancel,done:make(chan struct{}),stopTimeout:opts.StopTimeout}
	go func(){
		p.exitErr = cmd.Wait()
		close(p.done)
		cancel(errors.New("private worker exited"))
	}()

	// Startup timeout is distinct from the lifetime. Canceling closes
	// the parent pipe ends through OpenSession, unblocking handshake I/O.
	timer := time.AfterFunc(opts.StartupTimeout,func(){cancel(ErrStartupTimeout)})
	session, err := OpenSession(lifetime,parentRead,parentWrite,Library,handler)
	stopped := timer.Stop()
	if err != nil || !stopped || lifetime.Err()!=nil {
		cancel(err)
		if session!=nil {session.Close()}
		_ = parentRead.Close()
		_ = parentWrite.Close()
		_ = cmd.Process.Kill()
		<-p.done
		if cause:=context.Cause(lifetime);cause!=nil {return nil,fmt.Errorf("start private worker: %w",cause)}
		return nil,fmt.Errorf("start private worker: %w",err)
	}
	p.Session = session
	// Session loss must not leave the direct child running. Give a
	// cooperative child a bounded EOF grace period. Parent context
	// cancellation also triggers CommandContext's immediate kill.
	go func(){
		select{
		case <-p.done:
			session.Close()
		case <-session.Done():
			t:=time.NewTimer(p.stopTimeout)
			defer t.Stop()
			select{
			case <-p.done:
			case <-t.C:
				_ = cmd.Process.Kill()
				<-p.done
			}
		}
	}()
	return p,nil
}

// Stop asks the worker to drain. An acknowledgement is not a claim of
// durable application shutdown until the real handler implements draining.
// The first caller owns the shutdown attempt; others share its result.
// On timeout/refusal the direct child is terminated and reaped.
func (p *OwnedProcess) Stop(ctx context.Context) error {
	p.stopOnce.Do(func(){
		select{
		case <-p.done:
			p.stopErr=p.exitErr
			return
		default:
		}
		deadline,cancel:=context.WithTimeout(ctx,p.stopTimeout)
		defer cancel()
		e,err:=p.Session.Call(deadline,Shutdown,nil)
		if err==nil && e.Error!=nil {err=ErrShutdownRefused}
		if err==nil {
			p.Session.Close()
			select{
			case <-p.done:
				p.stopErr=p.exitErr
				return
			case <-deadline.Done():
				err=deadline.Err()
			}
		}
		p.Session.Close()
		p.cancel(err)
		select{
		case <-p.done:
		default:
			_ = p.cmd.Process.Kill()
			<-p.done
		}
		p.stopErr=errors.Join(ErrForcedStop,err)
	})
	return p.stopErr
}
