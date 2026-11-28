package acquisitionplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)
func fixtureDigest(s string) string {
	d:=sha256.Sum256([]byte(s))
	return hex.EncodeToString(d[:])
}
func runtimeTreeFixture(t *testing.T)(string,RuntimeManifest,RuntimeIdentity){
	t.Helper()
	root,err:=filepath.EvalSymlinks(t.TempDir())
	if err!=nil{t.Fatal(err)}
	entry:="gallery-dl"
	if runtime.GOOS=="windows"{entry+=".exe"}
	if err:=os.WriteFile(filepath.Join(root,entry),[]byte("fixture executable"),0700);err!=nil{t.Fatal(err)}
	if err:=os.Mkdir(filepath.Join(root,"lib"),0700);err!=nil{t.Fatal(err)}
	if err:=os.WriteFile(filepath.Join(root,"lib","dependency"),[]byte("fixture dependency"),0600);err!=nil{t.Fatal(err)}
	m:=RuntimeManifest{Version:StrictQualifiedVersion,Entry:entry,Files:[]RuntimeFile{{"lib/dependency",fixtureDigest("fixture dependency")},{entry,fixtureDigest("fixture executable")}}}
	d,err:=RuntimeManifestDigest(m)
	if err!=nil{t.Fatal(err)}
	return root,m,RuntimeIdentity{Generation:1,Version:m.Version,ArtifactSHA256:fixtureDigest("fixture executable"),ManifestSHA256:d}
}
func TestVerifyRuntimeTree(t *testing.T){
	root,m,id:=runtimeTreeFixture(t)
	ctx:=context.Background()
	if err:=VerifyRuntimeTree(ctx,root,m,id);err!=nil{t.Fatal(err)}
	m.Files[0],m.Files[1]=m.Files[1],m.Files[0]
	if err:=VerifyRuntimeTree(ctx,root,m,id);err!=nil{t.Fatal("manifest order changed identity",err)}
	for _,change:=range []func(*RuntimeIdentity){
		func(i *RuntimeIdentity){i.ManifestSHA256=fixtureDigest("wrong")},
		func(i *RuntimeIdentity){i.ArtifactSHA256=fixtureDigest("wrong")},
		func(i *RuntimeIdentity){i.Version="1.32.21"},
		func(i *RuntimeIdentity){i.Generation=0},
	}{
		bad:=id;change(&bad)
		if VerifyRuntimeTree(ctx,root,m,bad)==nil{t.Fatal("wrong identity")}
	}
	cancel,stop:=context.WithCancel(ctx);stop()
	if VerifyRuntimeTree(cancel,root,m,id)==nil{t.Fatal("canceled verification accepted")}
	if err:=os.WriteFile(filepath.Join(root,"extra"),[]byte("unlisted"),0600);err!=nil{t.Fatal(err)}
	if VerifyRuntimeTree(ctx,root,m,id)==nil{t.Fatal("unlisted file accepted")}
	if err:=os.Remove(filepath.Join(root,"extra"));err!=nil{t.Fatal(err)}
	if err:=os.WriteFile(filepath.Join(root,"lib","dependency"),[]byte("changed dependency"),0600);err!=nil{t.Fatal(err)}
	if VerifyRuntimeTree(ctx,root,m,id)==nil{t.Fatal("changed dependency accepted")}
	if err:=os.Remove(filepath.Join(root,"lib","dependency"));err!=nil{t.Fatal(err)}
	if VerifyRuntimeTree(ctx,root,m,id)==nil{t.Fatal("missing dependency accepted")}
}
func TestRuntimeManifestRefusesUnsafePaths(t *testing.T){
	_,m,_:=runtimeTreeFixture(t)
	for _,p:=range []string{"../outside","/absolute","lib/../other","lib\\other","lib/x:stream","lib/DEPENDENCY","LIB/new","lib","gallery-dl/other","lib/trailing."}{
		bad:=m
		bad.Files=append(append([]RuntimeFile(nil),m.Files...),RuntimeFile{p,fixtureDigest("x")})
		// gallery-dl/other is a conflict only on Unix.
		if p=="gallery-dl/other"&&runtime.GOOS=="windows"{continue}
		if _,err:=RuntimeManifestDigest(bad);err==nil{t.Fatalf("accepted manifest path %q",p)}
	}
}
func TestVerifyRuntimeTreeRejectsSymlink(t *testing.T){
	root,m,id:=runtimeTreeFixture(t)
	dep:=filepath.Join(root,"lib","dependency")
	outside:=filepath.Join(t.TempDir(),"dependency")
	if err:=os.WriteFile(outside,[]byte("fixture dependency"),0600);err!=nil{t.Fatal(err)}
	if err:=os.Remove(dep);err!=nil{t.Fatal(err)}
	if err:=os.Symlink(outside,dep);err!=nil{
		if runtime.GOOS=="windows"{t.Skip("symlink creation unavailable",err)}
		t.Fatal(err)
	}
	if VerifyRuntimeTree(context.Background(),root,m,id)==nil{t.Fatal("symlink accepted")}
}
