//go:build darwin || linux

package processguard

import (
	"bufio"
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// The worker waits for EOF on its stdin and exits normally. It performs
// no WAN work or child launch. The guard then kills its own group after
// reaping this worker, allowing the parent to verify group disappearance.
func TestDormantSupervisorFixture(t *testing.T) {
	switch os.Getenv("MEBOORU_GUARD_FIXTURE") {
	case "supervisor-guard":
		life := os.NewFile(3,"parent-liveness")
		worker := fixtureCommand(t,"TestDormantSupervisorFixture","supervisor-worker")
		worker.Stdin = os.Stdin
		worker.Stdout = os.Stdout
		_ = RunDormantGuard(life,worker)
		os.Exit(2)
	case "supervisor-worker":
		life := os.NewFile(3,"guard-liveness")
		done,err := WatchGuard(life)
		if err != nil { os.Exit(3) }
		_,_ = os.Stdout.WriteString("ready\n")
		eof := make(chan struct{})
		go func(){ var b [1]byte; _,_=os.Stdin.Read(b[:]); close(eof) }()
		select {
		case <-eof: os.Exit(0)
		case <-done: os.Exit(4)
		case <-time.After(20*time.Second): os.Exit(5)
		}
	}
}

func TestDormantSupervisorActivationAndCleanupProof(t *testing.T) {
	for _, arm := range []bool{false,true} {
		name := "never-armed"
		if arm { name = "armed" }
		t.Run(name,func(t *testing.T){
			ctx,cancel := context.WithTimeout(context.Background(),15*time.Second)
			defer cancel()
			cmd := fixtureCommand(t,"TestDormantSupervisorFixture","supervisor-guard")
			in,err := cmd.StdinPipe()
			if err != nil { t.Fatal(err) }
			defer in.Close()
			r,w,err := os.Pipe()
			if err != nil { t.Fatal(err) }
			defer r.Close()
			defer w.Close()
			cmd.Stdout=w
			g,err := StartDormantGuard(cmd)
			if g != nil {
				defer func(){ _ = in.Close(); _ = g.Stop(ctx) }()
			}
			if err != nil { t.Fatal(err) }
			_ = w.Close()
			if group,err := g.GroupID(); err != nil || group != cmd.Process.Pid { t.Fatalf("group: %d %v",group,err) }
			if arm {
				if err:=g.Activate();err!=nil{t.Fatal(err)}
				if err:=g.Activate();!errors.Is(err,ErrActivationRefused){t.Fatalf("duplicate activation: %v",err)}
				reader:=witnessReady(t,ctx,r)
				// Cooperative worker exit, rather than depending on
				// orphan reaping by the host init process.
				_ = in.Close()
				witnessEOF(t,ctx,reader)
				select {
				case <-g.Done():
				case <-ctx.Done():t.Fatal(ctx.Err())
				}
			}
			if err:=g.Stop(ctx);err!=nil{t.Fatal(err)}
			if !arm { witnessEOF(t,ctx,bufio.NewReader(r)) }
			if err:=g.Activate();!errors.Is(err,ErrActivationRefused){t.Fatalf("reactivation: %v",err)}
			if err:=g.Stop(ctx);err!=nil{t.Fatal(err)}
		})
	}
}
