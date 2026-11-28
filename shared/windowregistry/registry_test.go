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


func TestSubmissionCanOnlyBindOneExactJob(t *testing.T){
	ctx:=context.Background()
	path,err:=filepath.EvalSymlinks(t.TempDir());if err!=nil{t.Fatal(err)}
	r,err:=Open(path);if err!=nil{t.Fatal(err)}
	cmd:=workerproto.QueueCommand{URL:"https://derpibooru.org/search?q=fixture",Gallery:"A",MaxItems:3}
	sub,err:=r.NewSubmission(ctx,cmd,"stable-A");if err!=nil{t.Fatal(err)}
	if sub.Command.IntentID==""{t.Fatal("missing intent")}
	q:=workerproto.MetadataPlanRequest{JobID:17,Gallery:cmd.Gallery,Kind:workerproto.MetadataStrictResolve,Source:"derpibooru",URL:cmd.URL,First:1,Last:3}
	cfg:=acquisitionplan.MetadataConfig{Generation:1,Source:q.Source};ci,err:=cfg.Identity();if err!=nil{t.Fatal(err)}
	g:=Grant{Request:q,OwnerGeneration:strings.Repeat("a",64),PolicyGeneration:1,GalleryIdentity:"stable-A",IntentID:sub.Command.IntentID,Identity:acquisitionplan.MetadataIdentity{Config:ci,Runtime:acquisitionplan.RuntimeIdentity{Generation:1,Version:acquisitionplan.StrictQualifiedVersion,ArtifactSHA256:strings.Repeat("1",64),ManifestSHA256:strings.Repeat("2",64)}}}
	if err:=r.Register(ctx,g);err==nil{t.Fatal("unbound grant")}
	bad:=g;bad.GalleryIdentity="recreated-A"
	if err:=r.BindSubmission(ctx,bad);err==nil{t.Fatal("wrong stable gallery")}
	bad=g;bad.Request.Last++
	if err:=r.BindSubmission(ctx,bad);err==nil{t.Fatal("widened window")}
	if err:=r.Close();err!=nil{t.Fatal(err)}
	r,err=Open(path);if err!=nil{t.Fatal(err)};defer r.Close()
	got,err:=r.ObserveSubmission(sub.Command.IntentID);if err!=nil||got!=sub{t.Fatal(got,err)}
	if err:=r.BindSubmission(ctx,g);err!=nil{t.Fatal(err)}
	if _,err:=r.Claim(ctx,q,g.OwnerGeneration);err!=nil{t.Fatal(err)}
	bad=g;bad.Request.JobID++
	if err:=r.BindSubmission(ctx,bad);err==nil{t.Fatal("intent replay under new job")}
	if err:=r.RecordOutcome(ctx,Outcome{Grant:g,Kind:"no_transfer"});err!=nil{t.Fatal(err)}
	if err:=r.Settle(ctx,g,2,false);err!=nil{t.Fatal(err)}
	if err:=r.BindSubmission(ctx,g);err==nil{t.Fatal("settled intent replay")}
}
