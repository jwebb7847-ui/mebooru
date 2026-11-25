//go:build windows

package processguard

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Job owns an unnamed, non-inheritable Windows job handle with kill-on-close
// and no breakaway permission. The managed child must remain dormant until
// Assign succeeds; use the original Process handle, never a PID lookup.
type Job struct {
	mu sync.Mutex
	handle windows.Handle
	assigned bool
	sealed bool
}

func NewJob() (*Job,error) {
	h,err:=windows.CreateJobObject(nil,nil)
	if err!=nil{return nil,err}
	info:=windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags=windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_,err=windows.SetInformationJobObject(h,windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),uint32(unsafe.Sizeof(info)))
	runtime.KeepAlive(&info)
	if err!=nil{
		_ = windows.CloseHandle(h)
		return nil,err
	}
	return &Job{handle:h},nil
}

// Assign uses the stable handle held by os.Process before Wait/Release.
// Do not send a worker hello/activation message until assignment succeeds.
// A failed assignment must terminate and reap the still-dormant child.
func (j *Job) Assign(p *os.Process) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle==0||j.sealed||j.assigned{return errors.New("managed job unavailable for assignment")}
	var assignErr error
	err:=p.WithHandle(func(h uintptr){
		assignErr=windows.AssignProcessToJobObject(j.handle,windows.Handle(h))
	})
	if err==nil&&assignErr==nil{j.assigned=true}
	return errors.Join(err,assignErr)
}

// basicAccounting matches JOBOBJECT_BASIC_ACCOUNTING_INFORMATION.
type basicAccounting struct {
	TotalUserTime,TotalKernelTime,ThisPeriodTotalUserTime,ThisPeriodTotalKernelTime int64
	TotalPageFaultCount,TotalProcesses,ActiveProcesses,TotalTerminatedProcesses uint32
}

func (j *Job) activeLocked()(uint32,error){
	var info basicAccounting
	err:=windows.QueryInformationJobObject(j.handle,windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)),uint32(unsafe.Sizeof(info)),nil)
	runtime.KeepAlive(&info)
	return info.ActiveProcesses,err
}

// TerminateAndWait confirms that active membership reaches zero before
// returning success. A failure retains the handle and blocks replacement;
// Close remains a kill-on-close fallback, not proof of completed cleanup.
func(j *Job)TerminateAndWait(ctx context.Context)error{
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle==0{return errors.New("managed job already closed")}
	j.sealed=true
	if err:=windows.TerminateJobObject(j.handle,1);err!=nil{return err}
	ticker:=time.NewTicker(20*time.Millisecond)
	defer ticker.Stop()
	for{
		active,err:=j.activeLocked()
		if err!=nil{return err}
		if active==0{return nil}
		select{
		case <-ctx.Done():return ctx.Err()
		case <-ticker.C:
		}
	}
}

func(j *Job)Close()error{
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle==0{return nil}
	if err:=windows.CloseHandle(j.handle);err!=nil{return err}
	j.handle=0
	return nil
}
