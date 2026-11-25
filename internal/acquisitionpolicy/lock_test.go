package acquisitionpolicy

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"time"
	"path/filepath"
	"testing"
)

func TestProfileLockExcludesSecondOwnerAndReopens(t *testing.T){
	dir:=t.TempDir()
	first,err:=LockProfile(dir)
	if err!=nil{t.Fatal(err)}
	defer first.Close()
	second,err:=LockProfile(filepath.Join(dir,"."))
	if second!=nil||!errors.Is(err,ErrProfileOwned){if second!=nil{_ = second.Close()};t.Fatalf("second owner: %v",err)}
	if err:=first.Close();err!=nil{t.Fatal(err)}
	second,err=LockProfile(dir)
	if err!=nil{t.Fatal(err)}
	if err:=second.Close();err!=nil{t.Fatal(err)}
	if _,err:=os.Stat(filepath.Join(dir,lockFile));err!=nil{t.Fatal("persistent lock file missing: ",err)}
}

func TestProfileLockRefusesSymlink(t *testing.T){
	dir:=t.TempDir()
	outside:=filepath.Join(t.TempDir(),"outside")
	if err:=os.WriteFile(outside,[]byte("preserve"),0o600);err!=nil{t.Fatal(err)}
	if err:=os.Symlink(outside,filepath.Join(dir,lockFile));err!=nil{t.Skipf("symlink unavailable: %v",err)}
	l,err:=LockProfile(dir)
	if l!=nil{_ = l.Close()}
	if l!=nil||!errors.Is(err,ErrInvalidPolicy){t.Fatalf("symlink: %v",err)}
	if body,err:=os.ReadFile(outside);err!=nil||string(body)!="preserve"{t.Fatal("outside file changed")}
}


func TestProfileOwnerFixture(t *testing.T){
	dir:=os.Getenv("MEBOORU_TEST_LOCK_PROFILE")
	if dir==""{return}
	lock,err:=LockProfile(dir)
	if err!=nil{os.Exit(2)}
	_,_=os.Stdout.WriteString("ready\n")
	var one [1]byte
	_,_=os.Stdin.Read(one[:])
	// Intentionally bypass Close. The OS must release the lock.
	runtime.KeepAlive(lock)
	os.Exit(7)
}

func TestProfileLockOwnerProcessLoss(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
	defer cancel()
	dir:=t.TempDir()
	exe,err:=os.Executable()
	if err!=nil{t.Fatal(err)}
	cmd:=exec.CommandContext(ctx,exe,"-test.run=^TestProfileOwnerFixture$")
	cmd.Env=append(os.Environ(),"MEBOORU_TEST_LOCK_PROFILE="+dir)
	in,err:=cmd.StdinPipe()
	if err!=nil{t.Fatal(err)}
	defer in.Close()
	out,err:=cmd.StdoutPipe()
	if err!=nil{t.Fatal(err)}
	if err:=cmd.Start();err!=nil{t.Fatal(err)}
	waited:=false
	defer func(){if !waited{_ = cmd.Process.Kill();_ = cmd.Wait()}}()
	ch:=make(chan string,1)
	go func(){line,_:=bufio.NewReader(out).ReadString('\n');ch<-line}()
	select{case line:=<-ch:if line!="ready\n"{t.Fatalf("fixture: %q",line)};case <-ctx.Done():t.Fatal(ctx.Err())}
	if lock,err:=LockProfile(dir);lock!=nil||!errors.Is(err,ErrProfileOwned){if lock!=nil{_ = lock.Close()};t.Fatalf("other process owner: %v",err)}
	if err:=cmd.Process.Kill();err!=nil{t.Fatal(err)}
	_ = cmd.Wait()
	waited=true
	lock,err:=LockProfile(dir)
	if err!=nil{t.Fatal(err)}
	defer lock.Close()
}
