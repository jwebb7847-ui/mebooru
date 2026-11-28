package acquisitionpolicy

import(
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/windowregistry"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
)

func TestRegisterRunningWindowRequiresIntentAndDurableExactJob(t *testing.T){
	ctx:=context.Background()
	dir,err:=filepath.EvalSymlinks(t.TempDir());if err!=nil{t.Fatal(err)}
	db,err:=sql.Open("sqlite",filepath.Join(dir,"queue.sqlite"));if err!=nil{t.Fatal(err)};defer db.Close()
	if _,err=db.Exec("CREATE TABLE jobs(id INTEGER PRIMARY KEY,status TEXT,data TEXT)");err!=nil{t.Fatal(err)}
	q:=workerproto.MetadataPlanRequest{JobID:7,Gallery:"A",Kind:workerproto.MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/images/7",First:1,Last:1}
	record:=map[string]any{"job":map[string]any{"id":q.JobID,"url":q.URL,"gallery":q.Gallery,"site":q.Source,"max_items":1,"root":q.JobID,"status":"running"}}
	save:=func(status string){t.Helper();body,err:=json.Marshal(record);if err!=nil{t.Fatal(err)};if _,err:=db.Exec("INSERT OR REPLACE INTO jobs VALUES(?,?,?)",q.JobID,status,string(body));err!=nil{t.Fatal(err)}}
	reg,err:=windowregistry.Open(dir);if err!=nil{t.Fatal(err)};defer reg.Close()
	g:=windowregistry.Grant{Request:q,OwnerGeneration:strings.Repeat("a",64),PolicyGeneration:1,GalleryIdentity:"fixture-gallery"}
	cfg:=acquisitionplan.MetadataConfig{Generation:1,Source:g.Request.Source};ci,err:=cfg.Identity();if err!=nil{t.Fatal(err)}
	g.Identity=acquisitionplan.MetadataIdentity{Config:ci,Runtime:acquisitionplan.RuntimeIdentity{Generation:1,Version:acquisitionplan.StrictQualifiedVersion,ArtifactSHA256:strings.Repeat("1",64),ManifestSHA256:strings.Repeat("2",64)}}
	check:=func(_ context.Context,v windowregistry.Grant)bool{return v==g}
	save("queued")
	if err:=RegisterRunningWindow(ctx,dir,reg,g,check);err==nil{t.Fatal("queued authorized")}
	save("running")
	if err:=RegisterRunningWindow(ctx,dir,reg,g,func(context.Context,windowregistry.Grant)bool{return false});err==nil{t.Fatal("missing owner intent")}
	j:=record["job"].(map[string]any)
	j["gallery"]="B";save("running")
	if err:=RegisterRunningWindow(ctx,dir,reg,g,check);err==nil{t.Fatal("wrong gallery")}
	j["gallery"]="A";j["force"]=true;save("running")
	if err:=RegisterRunningWindow(ctx,dir,reg,g,check);err==nil{t.Fatal("unsupported force")}
	delete(j,"force");save("running")
	if err:=RegisterRunningWindow(ctx,dir,reg,g,check);err!=nil{t.Fatal(err)}
	if err:=RegisterRunningWindow(ctx,dir,reg,g,check);err==nil{t.Fatal("reissued grant")}
	if _,err:=reg.Claim(ctx,q,g.OwnerGeneration);err!=nil{t.Fatal(err)}
}


func TestRunningIntentRequiresExactWorkerAssignment(t *testing.T){
	ctx:=context.Background()
	dir,err:=filepath.EvalSymlinks(t.TempDir());if err!=nil{t.Fatal(err)}
	db,err:=sql.Open("sqlite",filepath.Join(dir,"queue.sqlite"));if err!=nil{t.Fatal(err)};defer db.Close()
	if _,err=db.Exec("CREATE TABLE jobs(id INTEGER PRIMARY KEY,status TEXT,data TEXT);CREATE TABLE managed_submissions(intent_id TEXT PRIMARY KEY,job_id INTEGER NOT NULL UNIQUE)");err!=nil{t.Fatal(err)}
	reg,err:=windowregistry.Open(dir);if err!=nil{t.Fatal(err)};defer reg.Close()
	cmd:=workerproto.QueueCommand{URL:"https://derpibooru.org/images/7",Gallery:"A",MaxItems:1}
	sub,err:=reg.NewSubmission(ctx,cmd,"stable-A");if err!=nil{t.Fatal(err)}
	q:=workerproto.MetadataPlanRequest{JobID:7,Gallery:cmd.Gallery,Kind:workerproto.MetadataStrictResolve,Source:"derpibooru",URL:cmd.URL,First:1,Last:1}
	cfg:=acquisitionplan.MetadataConfig{Generation:1,Source:q.Source};ci,err:=cfg.Identity();if err!=nil{t.Fatal(err)}
	g:=windowregistry.Grant{Request:q,OwnerGeneration:strings.Repeat("a",64),PolicyGeneration:1,GalleryIdentity:sub.GalleryIdentity,IntentID:sub.Command.IntentID,Identity:acquisitionplan.MetadataIdentity{Config:ci,Runtime:acquisitionplan.RuntimeIdentity{Generation:1,Version:acquisitionplan.StrictQualifiedVersion,ArtifactSHA256:strings.Repeat("1",64),ManifestSHA256:strings.Repeat("2",64)}}}
	check:=func(_ context.Context,v windowregistry.Grant)bool{return v==g}
	rec:=map[string]any{"managed_intent_id":g.IntentID,"job":map[string]any{"id":q.JobID,"root":q.JobID,"url":q.URL,"gallery":q.Gallery,"site":q.Source,"max_items":1,"status":"running"}}
	raw,err:=json.Marshal(rec);if err!=nil{t.Fatal(err)}
	if _,err:=db.Exec("INSERT INTO jobs VALUES(?,?,?)",q.JobID,"running",string(raw));err!=nil{t.Fatal(err)}
	if err:=RegisterRunningIntentWindow(ctx,dir,reg,g,check);err==nil{t.Fatal("absent assignment authorized")}
	if _,err:=db.Exec("INSERT INTO managed_submissions VALUES(?,?)",g.IntentID,q.JobID+1);err!=nil{t.Fatal(err)}
	if err:=RegisterRunningIntentWindow(ctx,dir,reg,g,check);err==nil{t.Fatal("different assignment authorized")}
	if _,err:=db.Exec("UPDATE managed_submissions SET job_id=?",q.JobID);err!=nil{t.Fatal(err)}
	if err:=RegisterRunningIntentWindow(ctx,dir,reg,g,check);err!=nil{t.Fatal(err)}
	if _,err:=reg.Claim(ctx,q,g.OwnerGeneration);err!=nil{t.Fatal(err)}
}
