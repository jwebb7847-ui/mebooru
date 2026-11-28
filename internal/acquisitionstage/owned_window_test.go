package acquisitionstage

import(
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/jobs"
	"github.com/monbooru/monbooru/internal/library"
)

type disposableWindowPeer struct{done chan struct{};offline bool}
func(p *disposableWindowPeer)Done()<-chan struct{}{return p.done}
func(p *disposableWindowPeer)ApplyOffline(_ context.Context,v bool)error{p.offline=v;return nil}
func(p *disposableWindowPeer)ReserveIdle(context.Context)(bool,error){return true,nil}
func(p *disposableWindowPeer)Stop(context.Context)error{select{case <-p.done:default:close(p.done)};return nil}

// Integrate real profile ownership/persisted consent, allocator, sealed
// policy, owner gallery fence, local commit and batch retirement. Only
// the worker transport/custody producer is synthetic; the separate pinned
// runtime fixtures exercise actual contained gallery-dl.
func TestDisposableOwnedWindow(t *testing.T){
	ctx:=context.Background()
	stage,s,q:=fixture(t)
	profile:=filepath.Join(filepath.Dir(stage),"profile")
	if err:=os.Mkdir(profile,0700);err!=nil{t.Fatal(err)}
	lock,err:=acquisitionpolicy.LockProfile(profile);if err!=nil{t.Fatal(err)}
	store,err:=acquisitionpolicy.Open(profile);if err!=nil{lock.Close();t.Fatal(err)}
	defer store.Close()
	policy,err:=acquisitionpolicy.New(store);if err!=nil{lock.Close();t.Fatal(err)}
	starts:=0
	manager,err:=acquisitionpolicy.NewManager(ctx,lock,policy,func(_, _ context.Context,f *acquisitionpolicy.WorkerFence)(acquisitionpolicy.ManagedPeer,error){
		if err:=f.Arm();err!=nil{return nil,err};starts++
		return &disposableWindowPeer{done:make(chan struct{}),offline:true},nil
	},acquisitionpolicy.LifecycleOptions{})
	if err!=nil{lock.Close();t.Fatal(err)}
	defer manager.Shutdown(ctx)
	var data bytes.Buffer
	if err:=png.Encode(&data,image.NewRGBA(image.Rect(0,0,16,16)));err!=nil{t.Fatal(err)}
	manifest:=[]byte(`{"42":{"category":"derpibooru","subcategory":"search","tags":["safe","fixture"],"meloader_verified_media_url":"https://example.invalid/42.png"}}`)
	meta:=[]byte(`{"id":42,"category":"derpibooru","subcategory":"search","tags":["safe","fixture"],"meloader_verified_media_url":"https://example.invalid/42.png"}`)
	q.Transfer.ManifestSHA256=digestBytes(manifest)
	s.MaxFiles=1;s.MaxBytes=4096
	allowed:=map[string]workerproto.StageAllocateRequest{q.OperationID:q}
	authority:=func(_ context.Context,v workerproto.StageAllocateRequest)(Scope,error){if allowed[v.OperationID]!=v{return Scope{},ErrStageRefused};return s,nil}
	a,err:=OpenForDestination(ctx,[]string{s.GalleryRoot,stage},2,16384,authority,q);if err!=nil{t.Fatal(err)}
	defer a.Close()
	a.freeBytes=func(string)(uint64,error){return 1<<40,nil}
	cfg:=config.Gallery{Name:"A",GalleryPath:s.GalleryRoot,DBPath:filepath.Join(filepath.Dir(stage),"gallery.sqlite"),ThumbnailsPath:filepath.Join(filepath.Dir(stage),"thumbs")}
	set,err:=library.OpenSet(ownerFixtureHost{cfg},jobs.NewManager(),[]config.Gallery{cfg},cfg.Name);if err!=nil{t.Fatal(err)}
	defer set.Close()
	row:=acquisitionplan.ApprovedPolicyRow{PostID:"42",NativeSHA256:digestBytes(meta),MediaURL:"https://example.invalid/42.png",SourceLabel:"derpibooru",PostURL:"https://derpibooru.org/images/42",Rating:"general",Tags:[]string{"rating:general","fixture"},Provenance:&acquisitionplan.ApprovedProvenance{}}
	body,hash,err:=acquisitionplan.EncodeApprovedPolicy(q.Transfer,[]acquisitionplan.ApprovedPolicyRow{row});if err!=nil{t.Fatal(err)}
	var files []acquisitionplan.VerifiedTransferFile
	var stopped bool
	owner:=OwnedWindow{
		Authorize:func(context.Context,workerproto.StageAllocation)error{return nil},
		Transfer:func(c context.Context,peer acquisitionpolicy.ManagedPeer,g workerproto.StageAllocation,path string)error{
			if peer.(*disposableWindowPeer).offline{return ErrStageRefused}
			cmd,err:=acquisitionplan.PrepareTransferCommands(g.Request.Transfer,manifest,acquisitionplan.MetadataConfig{Generation:1,Source:g.Request.Transfer.Metadata.Source},path);if err!=nil{return err}
			p:=filepath.Join(path,"media","42.png")
			if err:=os.WriteFile(p,data.Bytes(),0600);err!=nil{return err}
			if err:=os.WriteFile(p+".json",meta,0600);err!=nil{return err}
			files,err=cmd.VerifyTransferResult(c,[]byte(p+"\n"));stopped=err==nil
			return err
		},
		ConfirmStopped:func(context.Context,workerproto.StageAllocation)([]byte,[]acquisitionplan.VerifiedTransferFile,error){if !stopped{return nil,nil,ErrStageRefused};return manifest,files,nil},
		Policy:func(context.Context,workerproto.StageAllocation)([]byte,string,error){return body,hash,nil},
		Batch:BatchOwner{
			Current:func(context.Context,workerproto.StageAllocation)error{return nil},
			Child:func(_ context.Context,_ workerproto.StageAllocation,req workerproto.StageAllocateRequest,row acquisitionplan.ApprovedPolicyRow)(OwnerCommit,error){
				allowed[req.OperationID]=req
				return OwnerCommit{
					Current:func(context.Context,workerproto.StageAllocation)error{return nil},
					Disposition:func(context.Context,workerproto.StageAllocation)(string,error){return OwnerDispositionNew,nil},
					Naming:func(context.Context,workerproto.StageAllocation)(gallery.ReceivedNamingRequest,error){return gallery.ReceivedNamingRequest{Gallery:"A",ReceivedName:"42.png",Source:row.SourceLabel,PostID:row.PostID,DefaultName:"{source}-{post_id}"},nil},
					Extras:func(context.Context,workerproto.StageAllocation)(contract.JobExtras,error){return contract.JobExtras{},nil},
					Cleanup:func(context.Context,workerproto.StageAllocation,OwnerReceipt)error{return nil},
				},nil
			},
		},
		Reclaim:func(context.Context,workerproto.StageAllocation,BatchOutcome)error{return nil},
	}
	if _,err:=a.RunOwnedWindow(ctx,manager,set,q,owner);err==nil||starts!=0{t.Fatal("offline startup")}
	if err:=manager.SetOffline(ctx,false);err!=nil{t.Fatal(err)}
	out,err:=a.RunOwnedWindow(ctx,manager,set,q,owner)
	if err!=nil||len(out.Items)!=1||out.Items[0].State!="committed"||out.Items[0].Receipt==nil{t.Fatal("window",out,err)}
	w:=out.Items[0].Receipt
	got,err:=os.ReadFile(filepath.Join(s.GalleryRoot,w.Row.Name));if err!=nil||!bytes.Equal(got,data.Bytes()){t.Fatal("final bytes",err)}
	if err:=manager.SetOffline(ctx,true);err!=nil{t.Fatal(err)}
	if _,err:=a.RunOwnedWindow(ctx,manager,set,q,owner);err==nil{t.Fatal("offline replay")}
	if err:=manager.Shutdown(ctx);err!=nil{t.Fatal(err)}
	if off,err:=store.Load();err!=nil||!off{t.Fatal("persisted offline",off,err)}
}

func TestDestinationVerifiedCopy(t *testing.T){
	ctx:=context.Background()
	stage,s,q:=fixture(t)
	src:=filepath.Join(filepath.Dir(stage),"source")
	if err:=os.Mkdir(src,0700);err!=nil{t.Fatal(err)}
	manifest:=[]byte(`{"42":{"category":"derpibooru","subcategory":"search","tags":["safe"],"meloader_verified_media_url":"https://example.invalid/42.png"}}`)
	q.Transfer.ManifestSHA256=digestBytes(manifest)
	cmd,err:=acquisitionplan.PrepareTransferCommands(q.Transfer,manifest,acquisitionplan.MetadataConfig{Generation:1,Source:q.Transfer.Metadata.Source},src);if err!=nil{t.Fatal(err)}
	p:=filepath.Join(src,"media","42.png")
	data:=[]byte("verified fixture bytes")
	meta:=[]byte(`{"id":42,"category":"derpibooru","subcategory":"search","tags":["safe"],"meloader_verified_media_url":"https://example.invalid/42.png"}`)
	if err:=os.WriteFile(p,data,0600);err!=nil{t.Fatal(err)}
	if err:=os.WriteFile(p+".json",meta,0600);err!=nil{t.Fatal(err)}
	files,err:=cmd.VerifyTransferResult(ctx,[]byte(p+"\n"));if err!=nil{t.Fatal(err)}
	a,err:=OpenForDestination(ctx,[]string{s.GalleryRoot,stage},2,4096,func(context.Context,workerproto.StageAllocateRequest)(Scope,error){return s,nil},q);if err!=nil{t.Fatal(err)}
	defer a.Close()
	g,err:=a.Allocate(ctx,q);if err!=nil{t.Fatal(err)}
	e,err:=a.AdoptVerifiedWorkspace(ctx,g,src,func(context.Context,workerproto.StageAllocation)([]byte,[]acquisitionplan.VerifiedTransferFile,error){return manifest,files,nil})
	if err!=nil||len(e.Files)!=1||e.Files[0]!=files[0]{t.Fatal("adopt",e,err)}
	if b,err:=os.ReadFile(p);err!=nil||!bytes.Equal(b,data){t.Fatal("source changed",err)}
}
