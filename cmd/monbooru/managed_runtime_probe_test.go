package main

import(
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRuntimeProbeOutputBoundAndResult(t *testing.T){
	var out probeOutput
	n,err:=io.Copy(&out,strings.NewReader(strings.Repeat("x",runtimeProbeOutputLimit+100)))
	if err!=nil||n!=runtimeProbeOutputLimit+100||len(out.Bytes())!=runtimeProbeOutputLimit||!out.overflow{t.Fatalf("bound: %d %v %d %v",n,err,len(out.Bytes()),out.overflow)}
	for _,r:=range []runtimeVersionResult{
		{Stdout:[]byte("1.32.19")},
		{Started:true,ExitCode:1,Stdout:[]byte("1.32.19")},
		{Started:true,Failed:true,Stdout:[]byte("1.32.19")},
		{Started:true,Overflow:true,Stdout:[]byte("1.32.19")},
		{Started:true,Stderr:[]byte("warning"),Stdout:[]byte("1.32.19")},
		{Started:true,Stdout:[]byte("version 1.32.19")},
	}{
		if _,err:=r.version();err==nil{t.Fatalf("accepted failed result: %+v",r)}
	}
	for _,v:=range []string{"1.32.19","1.32.20","2.0.0"}{
		r:=runtimeVersionResult{Started:true,Stdout:[]byte(v+"\n")}
		if got,err:=r.version();err!=nil||got!=v{t.Fatalf("version independent output: %q %v",got,err)}
	}
}
func TestRuntimeProbeIdentityRejectsMismatch(t *testing.T){
	root,err:=filepath.EvalSymlinks(t.TempDir())
	if err!=nil{t.Fatal(err)}
	name:="gallery-dl"
	if runtime.GOOS=="windows"{name+=".exe"}
	path:=filepath.Join(root,name)
	body:=[]byte("harmless identity fixture, never executed")
	if err:=os.WriteFile(path,body,0700);err!=nil{t.Fatal(err)}
	sum:=sha256.Sum256(body)
	digest:=hex.EncodeToString(sum[:])
	if got,err:=verifyRuntimeProbeBinary(root,digest);err!=nil||got!=path{t.Fatalf("identity: %s %v",got,err)}
	if _,err:=verifyRuntimeProbeBinary(root,strings.Repeat("0",64));err==nil{t.Fatal("mismatched digest accepted")}
	if _,err:=verifyRuntimeProbeBinary(root,strings.ToUpper(digest));err==nil{t.Fatal("noncanonical digest accepted")}
	if runtime.GOOS!="windows"{
		target:=filepath.Join(root,"other")
		if err:=os.Rename(path,target);err!=nil{t.Fatal(err)}
		if err:=os.Symlink(target,path);err!=nil{t.Fatal(err)}
		if _,err:=verifyRuntimeProbeBinary(root,digest);err==nil{t.Fatal("symlink executable accepted")}
	}
}
