package acquisitionpolicy

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

const lockFile = "acquisition-owner.lock"

var ErrProfileOwned = errors.New("managed acquisition profile already owned")

// ProfileLock is an advisory OS lock for cooperative library instances.
// It is held for the lifetime of the supervisor and never inherited or
// unlinked. It does not prevent another same-user application from
// ignoring the lock; legacy standalone mode requires an explicit handoff.
type ProfileLock struct {
	mu sync.Mutex
	file *os.File
}

// LockProfile rejects path aliases by locking the actual opened inode/file,
// rather than using a PID file or TCP port. The profile directory must
// already exist. Do not remove the persistent lock file on release.
func LockProfile(profileDir string)(*ProfileLock,error){
	if !filepath.IsAbs(profileDir){return nil,errors.New("profile lock requires absolute path")}
	root,err:=os.OpenRoot(profileDir)
	if err!=nil{return nil,err}
	defer root.Close()
	info,err:=root.Lstat(lockFile)
	var f *os.File
	if errors.Is(err,os.ErrNotExist){
		f,err=root.OpenFile(lockFile,os.O_CREATE|os.O_EXCL|os.O_RDWR,0o600)
		if errors.Is(err,os.ErrExist){
			info,err=root.Lstat(lockFile)
		}else if err==nil{
			info,err=f.Stat()
		}
	}
	if err!=nil{
		if f!=nil{_ = f.Close()}
		return nil,err
	}
	if !info.Mode().IsRegular(){
		if f!=nil{_ = f.Close()}
		return nil,ErrInvalidPolicy
	}
	if f==nil{
		f,err=root.OpenFile(lockFile,os.O_RDWR,0)
		if err!=nil{return nil,err}
	}
	ok:=false
	defer func(){if !ok{_ = f.Close()}}()
	opened,err:=f.Stat()
	if err!=nil{return nil,err}
	if !opened.Mode().IsRegular()||!os.SameFile(info,opened){return nil,ErrInvalidPolicy}
	if err:=tryLock(f);err!=nil{return nil,err}
	current,err:=root.Lstat(lockFile)
	if err!=nil{return nil,err}
	if !current.Mode().IsRegular()||!os.SameFile(current,opened){return nil,ErrInvalidPolicy}
	ok=true
	return &ProfileLock{file:f},nil
}

// Closing the original descriptor releases its OS lock. Retain the
// lock while process cleanup is unconfirmed. Never delete the lock file.
func(l *ProfileLock)Close()error{
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file==nil{return nil}
	err:=l.file.Close()
	l.file=nil
	return err
}
