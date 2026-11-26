//go:build darwin || linux

// Package processguard contains isolated owned-process containment
// primitives. It is not a general same-user sandbox.
package processguard

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
)

var ErrGroupIdentity = errors.New("managed process group identity not established")

// PrepareGuard creates a new process group at guard launch. The guard
// executable must immediately enter RunGuard, not ordinary app startup.
// Its parent owns a dedicated liveness pipe writer, never inherited by
// the guard or worker. Do not combine this with other SysProcAttr fields.
func PrepareGuard(cmd *exec.Cmd) error {
	if cmd.SysProcAttr != nil || cmd.Process != nil {
		return ErrGroupIdentity
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid:true}
	return nil
}

// killOwnGroup uses only a group in which this live process is a member.
// No previously recorded PID or PGID is used to target another group.
// SIGKILL includes this process. If the OS refuses, report the error;
// the caller must not claim successful containment.
func killOwnGroup(group int) error {
	current, err := syscall.Getpgid(0)
	if err != nil || group <= 1 || current != group {
		return ErrGroupIdentity
	}
	return syscall.Kill(-group,syscall.SIGKILL)
}

// ActivationByte is sent over the exclusive guard liveness pipe only
// after the supervisor has recorded the verified group identity. It is
// an internal readiness boundary, not authentication.
const ActivationByte byte = 1

var ErrActivationRefused = errors.New("managed guard activation refused")

// RunDormantGuard establishes a no-worker-before-activation boundary.
// The supervisor must persist the established local group identity and
// retain exclusive ownership before writing ActivationByte. If the
// supervisor exits before that point, EOF exits without spawning a worker.
// The same pipe subsequently serves as the parent liveness pipe; every
// further byte or EOF revokes liveness. The worker still must install
// WatchGuard and start with default-deny network admission.
func RunDormantGuard(parentLife *os.File, worker *exec.Cmd) error {
	return runDormantGuard(parentLife,worker,nil)
}

// RunDormantGuardWithBroker transfers only the adopted broker ends to
// the worker, preserving fd 3 for guard liveness. No other inherited
// endpoints may be forwarded.
func RunDormantGuardWithBroker(parentLife *os.File,worker *exec.Cmd,r,w *os.File)error{
	if err:=validateProbePipes(r,w);err!=nil{return err}
	return runDormantGuard(parentLife,worker,[]*os.File{r,w})
}
func runDormantGuard(parentLife *os.File, worker *exec.Cmd,extra []*os.File) error {
	if parentLife == nil || worker == nil {
		return ErrActivationRefused
	}
	defer parentLife.Close()
	group, err := syscall.Getpgid(0)
	if err != nil || group <= 1 || group != os.Getpid() {
		return ErrGroupIdentity
	}
	info, err := parentLife.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return ErrActivationRefused
	}
	syscall.CloseOnExec(int(parentLife.Fd()))
	var one [1]byte
	n, err := io.ReadFull(parentLife, one[:])
	if err != nil || n != 1 || one[0] != ActivationByte {
		return ErrActivationRefused
	}
	return runGuard(parentLife, worker,extra)
}

// RunGuard must execute in a dedicated process group leader. The worker
// command is constructed by trusted application code (never remote input).
// It inherits the group and an exclusive read end on fd 3. The worker MUST
// install WatchGuard before doing any work or spawning descendants. All
// managed descendants must remain in this group; no setsid or Setpgid.
// Parent EOF or worker exit terminates the whole group, including guard.
// An unexpected guard exit is handled by the live worker's WatchGuard.
// Simultaneous loss of both sentinels is not guaranteed to be contained.
func RunGuard(parentLife *os.File, worker *exec.Cmd) error {
	return runGuard(parentLife,worker,nil)
}
func runGuard(parentLife *os.File, worker *exec.Cmd,extra []*os.File) error {
	group,err:=syscall.Getpgid(0)
	if err!=nil || group!=os.Getpid() || group<=1 ||
		worker.SysProcAttr!=nil || len(worker.ExtraFiles)!=0 ||
		worker.Process!=nil {
		return ErrGroupIdentity
	}
	syscall.CloseOnExec(int(parentLife.Fd()))
	defer parentLife.Close()
	read,write,err:=os.Pipe()
	if err!=nil{return err}
	defer write.Close()
	defer read.Close()
	worker.ExtraFiles=append([]*os.File{read},extra...)
	if err:=worker.Start();err!=nil{return err}
	_ = read.Close()
	for _,f:=range extra{_ = f.Close()}
	// An EOF, unexpected byte, or read failure all revoke parent liveness.
	lost:=make(chan struct{},1)
	go func(){
		var one [1]byte
		_,_ = parentLife.Read(one[:])
		lost<-struct{}{}
	}()
	exited:=make(chan error,1)
	go func(){exited<-worker.Wait()}()
	select{
	case <-lost:
	case <-exited:
	}
	return killOwnGroup(group)
}

// WatchGuard verifies that the worker was launched by its own group leader
// before allowing work. The inherited fd must be excluded from every
// descendant's handle list. Unexpected EOF kills this live worker's group.
// The caller must treat an error from the watcher as fatal and must not
// ignore it or continue network work. Normally group termination never returns.
func WatchGuard(life *os.File) (<-chan error,error) {
	group,err:=syscall.Getpgid(0)
	if err!=nil || group<=1 || group!=os.Getppid() || group==os.Getpid() {
		return nil,ErrGroupIdentity
	}
	info,err:=life.Stat()
	if err!=nil || info.Mode()&os.ModeNamedPipe==0 {
		return nil,ErrGroupIdentity
	}
	syscall.CloseOnExec(int(life.Fd()))
	done:=make(chan error,1)
	go func(){
		defer life.Close()
		var one [1]byte
		_,err:=life.Read(one[:])
		// Only liveness is carried on this pipe; any result means revoke.
		if err==nil {err=io.ErrUnexpectedEOF}
		done<-errors.Join(err,killOwnGroup(group))
	}()
	return done,nil
}
