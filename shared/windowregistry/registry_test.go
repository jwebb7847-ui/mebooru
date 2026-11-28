package windowregistry

import(
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)
func TestExactWindowRegistryAcrossReopen(t *testing.T){
	ctx:=context.Background()
	path,err:=filepath.EvalSymlinks(t.TempDir());if err!=nil{t.Fatal(err)}
	r,err:=Open(path);if err!=nil{t.Fatal(err)}
	g:=Grant{Request:workerproto.MetadataPlanRequest{JobID:1,Gallery:"A",Kind:workerproto.MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/images/1",First:1,Last:1},OwnerGeneration:strings.Repeat("a",32),PolicyGeneration:1}
	if err:=r.Register(ctx,g);err!=nil{t.Fatal(err)}
	bad:=g.Request;bad.Gallery="B"
	if _,err:=r.Claim(ctx,bad,g.OwnerGeneration);err==nil{t.Fatal("changed job")}
	if got,err:=r.Claim(ctx,g.Request,g.OwnerGeneration);err!=nil||got!=g{t.Fatal(got,err)}
	if err:=r.Settle(ctx,g,6,false);err==nil{t.Fatal("settled before owner receipt")}
	if err:=r.Close();err!=nil{t.Fatal(err)}
	r,err=Open(path);if err!=nil{t.Fatal(err)};defer r.Close()
	if _,err:=r.Claim(ctx,g.Request,g.OwnerGeneration);err==nil{t.Fatal("replayed claim")}
	if !r.Current(g){t.Fatal("lost original claim")}
	v:=Outcome{Grant:g,Kind:"committed",PolicySHA256:strings.Repeat("b",64),PlanSHA256:strings.Repeat("c",64)}
	if err:=r.RecordOutcome(ctx,v);err!=nil{t.Fatal(err)}
	if err:=r.Settle(ctx,g,2,false);err==nil{t.Fatal("wrong phase")}
	if err:=r.Settle(ctx,g,6,false);err!=nil{t.Fatal(err)}
	if r.Current(g){t.Fatal("settled grant still current")}
	if err:=r.Register(ctx,g);err==nil{t.Fatal("reused job ID")}
	next:=g;next.Request.JobID++
	if err:=r.Register(ctx,next);err!=nil{t.Fatal(err)}
	if _,err:=r.Claim(ctx,next.Request,next.OwnerGeneration);err!=nil{t.Fatal(err)}
	if err:=r.RecordOutcome(ctx,Outcome{Grant:next,Kind:"no_transfer"});err!=nil{t.Fatal(err)}
	if err:=r.Settle(ctx,next,2,false);err!=nil{t.Fatal(err)}
}
