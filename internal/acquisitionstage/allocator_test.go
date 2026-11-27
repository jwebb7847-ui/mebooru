package acquisitionstage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

func fixture(t *testing.T)(string,Scope,workerproto.StageAllocateRequest){
	t.Helper()
	base,err:=filepath.EvalSymlinks(t.TempDir())
	if err!=nil{t.Fatal(err)}
	stage:=filepath.Join(base,"stage")
	gallery:=filepath.Join(base,"gallery")
	for _,p:=range []string{stage,gallery}{if err:=os.Mkdir(p,0700);err!=nil{t.Fatal(err)}}
	s:=Scope{GalleryIdentity:"fixture-stable-id",GalleryRoot:gallery,GalleryRoots:[]string{gallery},MaxFiles:2,MaxBytes:1024}
	q:=workerproto.StageAllocateRequest{OperationID:strings.Repeat("a",32),Transfer:workerproto.TransferPlanRequest{
		Metadata:workerproto.MetadataPlanRequest{JobID:7,Gallery:"A",Kind:workerproto.MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/search?q=fixture",First:1,Last:3},
		Subcategory:"search",PolicyGeneration:1,ManifestSHA256:strings.Repeat("b",64),
	}}
	return stage,s,q
}
func TestAllocatorExactReplayAndLimits(t *testing.T){
	ctx:=context.Background()
	stage,s,q:=fixture(t)
	authority:=func(_ context.Context,r workerproto.StageAllocateRequest)(Scope,error){
		if r.Transfer.Metadata.Gallery!="A"{return Scope{},ErrStageRefused};return s,nil
	}
	a,err:=Open(stage,2,2048,authority)
	if err!=nil{t.Fatal(err)}
	defer func(){_ = a.Close()}()
	grant,err:=a.Allocate(ctx,q)
	if err!=nil{t.Fatal(err)}
	path,err:=a.Workspace(ctx,grant)
	if err!=nil||filepath.Dir(path)!=filepath.Join(stage,q.OperationID){t.Fatalf("workspace %q %v",path,err)}
	if got,err:=a.Allocate(ctx,q);err!=nil||got!=grant{t.Fatal("replay widened allocation",err)}
	if err:=a.Close();err!=nil{t.Fatal(err)}
	a,err=Open(stage,2,2048,authority)
	if err!=nil{t.Fatal(err)}
	if got,err:=a.Allocate(ctx,q);err!=nil||got!=grant{t.Fatal("restart allocated a second workspace",err)}
	bad:=q;bad.Transfer.Metadata.JobID++
	if _,err:=a.Allocate(ctx,bad);!errors.Is(err,ErrStageRefused){t.Fatal("operation ID reused",err)}
	wrong:=grant;wrong.TransferID=strings.Repeat("c",32)
	if _,err:=a.Workspace(ctx,wrong);err==nil{t.Fatal("foreign transfer")}
	s.GalleryIdentity="replacement"
	if _,err:=a.Workspace(ctx,grant);err==nil{t.Fatal("gallery replacement retained scope")}
	s.GalleryIdentity="fixture-stable-id"
	s.MaxBytes=2048
	second:=q;second.OperationID=strings.Repeat("d",32)
	if _,err:=a.Allocate(ctx,second);!errors.Is(err,ErrStageLimit){t.Fatal("byte limit",err)}
	s.MaxBytes=1024
	if _,err:=a.Allocate(ctx,second);err!=nil{t.Fatal(err)}
	third:=q;third.OperationID=strings.Repeat("e",32)
	if _,err:=a.Allocate(ctx,third);!errors.Is(err,ErrStageLimit){t.Fatal("count limit",err)}
	canceled,cancel:=context.WithCancel(ctx);cancel()
	if _,err:=a.Workspace(canceled,grant);err==nil{t.Fatal("canceled scope")}
}
func TestAllocatorRejectsOverlapsAndAmbiguity(t *testing.T){
	ctx:=context.Background()
	for _,mode:=range []string{"inside gallery","contains gallery","missing destination","partial allocation","unexpected entry","symlink workspace","revoked","root replaced"}{
		t.Run(mode,func(t *testing.T){
			stage,s,q:=fixture(t)
			switch mode{
			case "inside gallery":
				s.GalleryRoots=append(s.GalleryRoots,filepath.Dir(stage))
			case "contains gallery":
				nested:=filepath.Join(stage,"nested")
				if err:=os.Mkdir(nested,0700);err!=nil{t.Fatal(err)}
				s.GalleryRoots=append(s.GalleryRoots,nested)
			case "missing destination":s.GalleryRoots=[]string{stage}
			case "partial allocation":
				if err:=os.Mkdir(filepath.Join(stage,q.OperationID),0700);err!=nil{t.Fatal(err)}
			case "unexpected entry":
				if err:=os.WriteFile(filepath.Join(stage,"unexpected"),[]byte("keep"),0600);err!=nil{t.Fatal(err)}
			}
			allowed:=true
			a,err:=Open(stage,2,2048,func(context.Context,workerproto.StageAllocateRequest)(Scope,error){
				if !allowed{return Scope{},ErrStageRefused};return s,nil
			})
			if err!=nil{t.Fatal(err)}
			defer a.Close()
			grant,err:=a.Allocate(ctx,q)
			switch mode{
			case "symlink workspace","revoked","root replaced":
				if err!=nil{t.Fatal(err)}
				path,err:=a.Workspace(ctx,grant)
				if err!=nil{t.Fatal(err)}
				switch mode{
				case "symlink workspace":
					if err:=os.Remove(path);err!=nil{t.Fatal(err)}
					if err:=os.Symlink(s.GalleryRoot,path);err!=nil{t.Skipf("symlink unavailable: %v",err)}
				case "revoked":allowed=false
				case "root replaced":
					if err:=os.Rename(stage,stage+"-old");err!=nil{t.Skipf("open directory rename unavailable: %v",err)}
					if err:=os.Mkdir(stage,0700);err!=nil{t.Fatal(err)}
				}
				if _,err:=a.Workspace(ctx,grant);err==nil{t.Fatal("unsafe workspace resolved")}
			default:
				if err==nil{t.Fatal("unsafe allocation accepted")}
			}
		})
	}
}

func TestAllocatorSpaceReservationAccountsForBothCopies(t *testing.T){
	ctx:=context.Background()
	stage,s,q:=fixture(t)
	a,err:=Open(stage,2,4096,func(context.Context,workerproto.StageAllocateRequest)(Scope,error){return s,nil})
	if err!=nil{t.Fatal(err)}
	defer a.Close()
	needed:=2*s.MaxBytes+reservationOverhead
	a.freeBytes=func(string)(uint64,error){return needed-1,nil}
	if _,err:=a.Allocate(ctx,q);!errors.Is(err,ErrStageLimit){t.Fatal("low space",err)}
	entries,err:=os.ReadDir(stage)
	if err!=nil||len(entries)!=0{t.Fatal("low-space allocation changed root",err)}
	a.freeBytes=func(string)(uint64,error){return 0,errors.New("unavailable volume")}
	if _,err:=a.Allocate(ctx,q);!errors.Is(err,ErrStageLimit){t.Fatal("unknown space",err)}
	a.freeBytes=func(string)(uint64,error){return needed,nil}
	g,err:=a.Allocate(ctx,q)
	if err!=nil{t.Fatal(err)}
	a.freeBytes=func(string)(uint64,error){return 0,nil}
	if got,err:=a.Allocate(ctx,q);err!=nil||got!=g{t.Fatal("existing allocation lost on low space",err)}
	second:=q;second.OperationID=strings.Repeat("c",32)
	a.freeBytes=func(string)(uint64,error){return 2*needed-1,nil}
	if _,err:=a.Allocate(ctx,second);!errors.Is(err,ErrStageLimit){t.Fatal("aggregate capacity",err)}
}
