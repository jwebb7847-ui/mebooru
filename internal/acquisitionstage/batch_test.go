package acquisitionstage

import(
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/jobs"
	"github.com/monbooru/monbooru/internal/library"
)

func TestOwnerBatchMixedAndArchive(t *testing.T){
	for _,partial:=range []bool{false,true}{
		t.Run(fmt.Sprint(partial),func(t *testing.T){
			ctx:=context.Background()
			stage,s,q:=fixture(t)
			q.Transfer.Metadata.Last=4
			s.MaxFiles=4;s.MaxBytes=16384
			native:=map[string]json.RawMessage{}
			var rows []acquisitionplan.ApprovedPolicyRow
			data:=map[string][]byte{}
			for i:=1;i<=4;i++{
				id:=strconv.Itoa(40+i)
				url:="https://example.invalid/"+id+".png"
				raw:=json.RawMessage(fmt.Sprintf(`{"category":"derpibooru","subcategory":"search","tags":["safe","fixture"],"meloader_verified_media_url":%q}`,url))
				native[id]=raw
				rows=append(rows,acquisitionplan.ApprovedPolicyRow{PostID:id,NativeSHA256:digestBytes(raw),MediaURL:url,SourceLabel:"derpibooru",PostURL:"https://derpibooru.org/images/"+id,Rating:"general",Tags:[]string{"fixture","rating:general"},Provenance:&acquisitionplan.ApprovedProvenance{}})
				var b bytes.Buffer
				if err:=png.Encode(&b,image.NewRGBA(image.Rect(0,0,16+i,16)));err!=nil{t.Fatal(err)}
				data[id]=b.Bytes()
			}
			manifest,err:=json.Marshal(native);if err!=nil{t.Fatal(err)}
			q.Transfer.ManifestSHA256=digestBytes(manifest)
			allowed:=map[string]workerproto.StageAllocateRequest{q.OperationID:q}
			authority:=func(_ context.Context,v workerproto.StageAllocateRequest)(Scope,error){
				if allowed[v.OperationID]!=v{return Scope{},ErrStageRefused}
				x:=s;if v!=q{x.MaxFiles=1;x.MaxBytes=4096};return x,nil
			}
			a,err:=Open(stage,2,32768,authority);if err!=nil{t.Fatal(err)}
			defer func(){a.Close()}()
			a.freeBytes=func(string)(uint64,error){return 1<<40,nil}
			parent,err:=a.Allocate(ctx,q);if err!=nil{t.Fatal(err)}
			workspace,err:=a.Workspace(ctx,parent);if err!=nil{t.Fatal(err)}
			cmd,err:=acquisitionplan.PrepareTransferCommands(q.Transfer,manifest,acquisitionplan.MetadataConfig{Generation:1,Source:q.Transfer.Metadata.Source},workspace);if err!=nil{t.Fatal(err)}
			var stdout bytes.Buffer
			for _,row:=range rows[:3]{
				id:=row.PostID
				file:=filepath.Join(workspace,"media",id+".png")
				var side map[string]json.RawMessage
				if json.Unmarshal(native[id],&side)!=nil{t.Fatal("native")}
				side["id"]=json.RawMessage(id)
				meta,err:=json.Marshal(side);if err!=nil{t.Fatal(err)}
				if err:=os.WriteFile(file,data[id],0600);err!=nil{t.Fatal(err)}
				if err:=os.WriteFile(file+".json",meta,0600);err!=nil{t.Fatal(err)}
				fmt.Fprintln(&stdout,file)
			}
			files,err:=cmd.VerifyTransferResult(ctx,stdout.Bytes());if err!=nil||len(files)!=3{t.Fatal("transfer",files,err)}
			if _,err:=a.Seal(ctx,parent,func(context.Context,workerproto.StageAllocation)([]byte,[]acquisitionplan.VerifiedTransferFile,error){return manifest,files,nil});err!=nil{t.Fatal(err)}
			body,hash,err:=acquisitionplan.EncodeApprovedPolicy(q.Transfer,rows);if err!=nil{t.Fatal(err)}
			if err:=a.BindPolicy(ctx,parent,func(context.Context,workerproto.StageAllocation)([]byte,string,error){return body,hash,nil});err!=nil{t.Fatal(err)}
			cfg:=config.Gallery{Name:"A",GalleryPath:s.GalleryRoot,DBPath:filepath.Join(filepath.Dir(stage),"gallery.sqlite"),ThumbnailsPath:filepath.Join(filepath.Dir(stage),"thumbs")}
			set,err:=library.OpenSet(ownerFixtureHost{cfg},jobs.NewManager(),[]config.Gallery{cfg},cfg.Name);if err!=nil{t.Fatal(err)}
			defer set.Close()
			g:=set.Get(cfg.Name)
			for _,id:=range []string{"42","43"}{
				p:=filepath.Join(s.GalleryRoot,"old-"+id+".png")
				if err:=os.WriteFile(p,data[id],0600);err!=nil{t.Fatal(err)}
				res,err:=g.LandInPlace(ctx,p,library.LandSpec{Origin:"fixture"});if err!=nil||res.Image==nil{t.Fatal("seed",err)}
				if id=="43"{if err:=os.Remove(p);err!=nil{t.Fatal(err)};if _,err:=gallery.SetMissing(g.DB,res.Image.ID,true);err!=nil{t.Fatal(err)}}
			}
			owner:=BatchOwner{
				Current:func(context.Context,workerproto.StageAllocation)error{return nil},
				Child:func(_ context.Context,_ workerproto.StageAllocation,req workerproto.StageAllocateRequest,row acquisitionplan.ApprovedPolicyRow)(OwnerCommit,error){
					if partial&&row.PostID=="42"{return OwnerCommit{},ErrStageRefused}
					allowed[req.OperationID]=req
					kind:=OwnerDispositionNew
					if row.PostID=="42"{kind=OwnerDispositionHeld};if row.PostID=="43"{kind=OwnerDispositionRestore}
					return OwnerCommit{
						Current:func(context.Context,workerproto.StageAllocation)error{return nil},
						Disposition:func(context.Context,workerproto.StageAllocation)(string,error){return kind,nil},
						Naming:func(context.Context,workerproto.StageAllocation)(gallery.ReceivedNamingRequest,error){return gallery.ReceivedNamingRequest{Gallery:cfg.Name,ReceivedName:row.PostID+".png",Source:row.SourceLabel,PostID:row.PostID,DefaultName:"{source}-{post_id}"},nil},
						Extras:func(context.Context,workerproto.StageAllocation)(contract.JobExtras,error){return contract.JobExtras{},nil},
						Cleanup:func(context.Context,workerproto.StageAllocation,OwnerReceipt)error{return nil},
					},nil
				},
			}
			out,err:=a.CommitBatchForOwner(ctx,parent,set,owner)
			if (err!=nil)!=partial||len(out.Items)!=4{t.Fatal("batch",out,err)}
			for i,v:=range out.Items{
				want:="committed";if i==3{want="not_transferred"}else if partial&&i>0{want="unresolved"}
				if v.State!=want||v.PostID!=rows[i].PostID{t.Fatal("outcome",v,want)}
			}
			again,err:=a.ObserveBatch(ctx,parent);if err!=nil||!reflect.DeepEqual(again,out){t.Fatal("observe",again,err)}
			if _,err:=a.CommitBatchForOwner(ctx,parent,set,owner);err==nil{t.Fatal("batch replay")}
			if partial{return}
			for _,v:=range out.Items{
				if v.Receipt==nil{continue}
				w:=*v.Receipt
				entry,err:=a.ArchiveClosed(ctx,w.Allocation,func(context.Context,workerproto.StageAllocation,OwnerReceipt)error{return nil})
				if err!=nil||entry.Closure.Witness!=w{t.Fatal("archive",err)}
				if _,err:=a.Allocate(ctx,w.Allocation.Request);err==nil{t.Fatal("archived identity replay")}
			}
			if err:=a.ReclaimBatch(ctx,parent,set,func(context.Context,workerproto.StageAllocation,BatchOutcome)error{return nil},false);err!=nil{t.Fatal("reclaim",err)}
			for _,f:=range files{
				if _,err:=os.Stat(filepath.Join(stage,parent.Request.OperationID,"sealed","media",f.RelativeName));!os.IsNotExist(err){t.Fatal("parent payload retained",err)}
			}
			if err:=a.Close();err!=nil{t.Fatal(err)}
			a,err=Open(stage,2,32768,authority);if err!=nil{t.Fatal(err)}
			again,err=a.ObserveBatch(ctx,parent);if err!=nil||!reflect.DeepEqual(again,out){t.Fatal("reopen",again,err)}
			_,closed,reserved,err:=a.inventoryWithClosure(ctx)
			var side uint64;for _,f:=range files{side+=uint64(f.SidecarSize)}
			if err!=nil||!closed[parent.Request.OperationID]||reserved!=2*side{t.Fatal("retained accounting",reserved,err)}
			if _,err:=a.ArchiveBatch(ctx,parent,func(context.Context,workerproto.StageAllocation,BatchOutcome)error{return nil});err!=nil{t.Fatal("archive parent",err)}
			again,err=a.ObserveBatch(ctx,parent);if err!=nil||!reflect.DeepEqual(again,out){t.Fatal("archived batch",err)}
			records,_,reserved,err:=a.inventoryWithClosure(ctx);if err!=nil||len(records)!=0||reserved!=2*side{t.Fatal("archive inventory",records,reserved,err)}
			if _,err:=a.Allocate(ctx,parent.Request);err==nil{t.Fatal("archived parent replay")}
		})
	}
}

// Verify deterministic bounded subdivision of a normal 200-post window.
// This is a planning test, not a 200-post real-runtime commit benchmark.
func TestBatchPlan200(t *testing.T){
	_,s,q:=fixture(t)
	q.Transfer.Metadata.Last=200
	var rows []acquisitionplan.ApprovedPolicyRow
	var files []acquisitionplan.VerifiedTransferFile
	native:=map[string]json.RawMessage{}
	for i:=1;i<=200;i++{
		id:=strconv.Itoa(i)
		url:="https://example.invalid/"+id+".png"
		raw:=json.RawMessage(fmt.Sprintf(`{"category":"derpibooru","subcategory":"search","tags":["fixture"],"meloader_verified_media_url":%q}`,url))
		native[id]=raw
		rows=append(rows,acquisitionplan.ApprovedPolicyRow{PostID:id,NativeSHA256:digestBytes(raw),MediaURL:url,SourceLabel:"derpibooru",PostURL:"https://derpibooru.org/images/"+id,Rating:"general",Tags:[]string{"rating:general"},Provenance:&acquisitionplan.ApprovedProvenance{}})
		files=append(files,acquisitionplan.VerifiedTransferFile{ID:id,RelativeName:id+".png",Size:1,SHA256:digestBytes([]byte(id)),SidecarSize:1,SidecarSHA256:digestBytes(raw)})
	}
	manifest,err:=json.Marshal(native);if err!=nil{t.Fatal(err)}
	q.Transfer.ManifestSHA256=digestBytes(manifest)
	grant:=workerproto.StageAllocation{Request:q,TransferID:"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",MaxFiles:200,MaxBytes:4096}
	r:=record{Version:1,Allocation:grant,GalleryIdentity:s.GalleryIdentity,GalleryRoot:s.GalleryRoot}
	e:=SealedEvidence{Version:1,Allocation:grant,Files:files}
	snapshot:=acquisitionplan.ApprovedPolicySnapshot{Version:1,Transfer:q.Transfer,Rows:rows}
	p,err:=makeBatchPlan(r,e,manifest,snapshot);if err!=nil||len(p.Items)!=200{t.Fatal("plan",err)}
	seen:=map[string]bool{}
	for i,x:=range p.Items{
		if x.PostID!=rows[i].PostID||x.File!=files[i]||seen[x.Request.OperationID]||!workerproto.ValidStageAllocate(x.Request){t.Fatal("invalid child",i)}
		seen[x.Request.OperationID]=true
	}
}
