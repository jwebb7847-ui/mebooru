package processguard

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestExtraPipeFixture(t *testing.T){
	token:=os.Getenv("MEBOORU_TEST_EXTRA_PIPES")
	if token==""{return}
	r,w,err:=OpenInheritedPipes(token)
	if err!=nil{os.Exit(2)}
	// stdio remains independent control, not protocol.
	if _,err:=os.Stdout.Write([]byte{7});err!=nil{os.Exit(3)}
	var b [1]byte
	if _,err:=io.ReadFull(r,b[:]);err!=nil||b[0]!=42{os.Exit(4)}
	if _,err:=w.Write([]byte{43});err!=nil{os.Exit(5)}
	if err:=r.Close();err!=nil{os.Exit(6)}
	if err:=w.Close();err!=nil{os.Exit(7)}
	// Protocol EOF must occur while the child is still alive.
	if _,err:=io.ReadFull(os.Stdin,b[:]);err!=nil||b[0]!=8{os.Exit(8)}
	os.Exit(0)
}

func TestIndependentInheritedPipes(t *testing.T){
	if runtime.GOOS!="darwin"&&runtime.GOOS!="linux"&&runtime.GOOS!="windows"{t.Skip("unsupported platform")}
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
	defer cancel()
	exe,err:=os.Executable()
	if err!=nil{t.Fatal(err)}
	cmd:=exec.CommandContext(ctx,exe,"-test.run=^TestExtraPipeFixture$")
	controlIn,err:=cmd.StdinPipe()
	if err!=nil{t.Fatal(err)}
	defer controlIn.Close()
	controlOut,err:=cmd.StdoutPipe()
	if err!=nil{t.Fatal(err)}
	defer controlOut.Close()
	childRead,parentWrite,err:=os.Pipe()
	if err!=nil{t.Fatal(err)}
	defer childRead.Close();defer parentWrite.Close()
	parentRead,childWrite,err:=os.Pipe()
	if err!=nil{t.Fatal(err)}
	defer parentRead.Close();defer childWrite.Close()
	token,release,err:=AttachInheritedPipes(cmd,childRead,childWrite)
	if err!=nil{t.Fatal(err)}
	defer release()
	if _,_,err:=AttachInheritedPipes(cmd,childRead,childWrite);!errors.Is(err,ErrInheritedPipes){t.Fatalf("duplicate attachment: %v",err)}
	cmd.Env=append(os.Environ(),"MEBOORU_TEST_EXTRA_PIPES="+token)
	if err:=cmd.Start();err!=nil{t.Fatal(err)}
	release()
	_ = childRead.Close()
	_ = childWrite.Close()
	waited:=false
	defer func(){if !waited{_ = cmd.Process.Kill();_ = cmd.Wait()}}()
	var b [1]byte
	if _,err:=io.ReadFull(controlOut,b[:]);err!=nil||b[0]!=7{t.Fatalf("control readiness: %v",err)}
	if _,err:=parentWrite.Write([]byte{42});err!=nil{t.Fatal(err)}
	if _,err:=io.ReadFull(parentRead,b[:]);err!=nil||b[0]!=43{t.Fatalf("protocol response: %v",err)}
	if _,err:=parentRead.Read(b[:]);!errors.Is(err,io.EOF){t.Fatalf("protocol EOF before control exit: %v",err)}
	if _,err:=controlIn.Write([]byte{8});err!=nil{t.Fatal(err)}
	_ = controlIn.Close()
	err=cmd.Wait()
	waited=true
	if err!=nil{t.Fatal(err)}
}

func TestInheritedPipesRejectNonPipeAndMalformedToken(t *testing.T){
	exe,err:=os.Executable()
	if err!=nil{t.Fatal(err)}
	f,err:=os.CreateTemp(t.TempDir(),"regular")
	if err!=nil{t.Fatal(err)}
	defer f.Close()
	r,w,err:=os.Pipe()
	if err!=nil{t.Fatal(err)}
	defer r.Close();defer w.Close()
	if _,_,err:=AttachInheritedPipes(exec.Command(exe),f,w);!errors.Is(err,ErrInheritedPipes){t.Fatalf("regular file: %v",err)}
	if _,_,err:=AttachInheritedPipes(exec.Command(exe),r,r);!errors.Is(err,ErrInheritedPipes){t.Fatalf("same endpoint: %v",err)}
	if _,_,err:=OpenInheritedPipes("not-a-bootstrap-token");!errors.Is(err,ErrInheritedPipes){t.Fatalf("malformed token: %v",err)}
}
