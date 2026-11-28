package windowregistry

import(
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
)
func TestExactWindowRegistryAcrossReopen(t *testing.T){
	ctx:=context.Background()
	path,err:=filepath.EvalSymlinks(t.TempDir());if err!=nil{t.Fatal(err)}
	r,err:=Open(path);if err!=nil{t.Fatal(err)}
	g:=Grant{Request:workerproto.MetadataPlanRequest{JobID:1,Gallery:"A",Kind:workerproto.MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/images/1",First:1,Last:1},OwnerGeneration:strings.Repeat("a",64),PolicyGeneration:1,GalleryIdentity:"fixture-gallery"}
	cfg:=acquisitionplan.MetadataConfig{Generation:1,Source:g.Request.Source};ci,err:=cfg.Identity();if err!=nil{t.Fatal(err)}
	g.Identity=acquisitionplan.MetadataIdentity{Config:ci,Runtime:acquisitionplan.RuntimeIdentity{Generation:1,Version:acquisitionplan.StrictQualifiedVersion,ArtifactSHA256:strings.Repeat("1",64),ManifestSHA256:strings.Repeat("2",64)}}
	badIdentity:=g;badIdentity.Identity.Config.SHA256=strings.Repeat("f",64)
	if err:=r.Register(ctx,badIdentity);err==nil{t.Fatal("invalid config identity")}
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
	if !r.Admitting(g){t.Fatal("missing admission")}
	if err:=r.RecordOutcome(ctx,v);err!=nil{t.Fatal(err)}
	if r.Admitting(g)||!r.Current(g){t.Fatal("outcome did not end admission while retaining claim")}
	if err:=r.Settle(ctx,g,2,false);err==nil{t.Fatal("wrong phase")}
	if err:=r.Settle(ctx,g,6,false);err!=nil{t.Fatal(err)}
	if r.Current(g){t.Fatal("settled grant still current")}
	if got,err:=r.ObserveSettlement(g);err!=nil||got!=v{t.Fatal("settlement",got,err)}
	if err:=r.Register(ctx,g);err==nil{t.Fatal("reused job ID")}
	next:=g;next.Request.JobID++
	if err:=r.Register(ctx,next);err!=nil{t.Fatal(err)}
	if _,err:=r.Claim(ctx,next.Request,next.OwnerGeneration);err!=nil{t.Fatal(err)}
	if err:=r.RecordOutcome(ctx,Outcome{Grant:next,Kind:"no_transfer"});err!=nil{t.Fatal(err)}
	if err:=r.Settle(ctx,next,2,false);err!=nil{t.Fatal(err)}
}
