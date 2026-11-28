package main

import(
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/jwebb7847-ui/mebooru/shared/managedqa"
	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/jwebb7847-ui/mebooru/shared/windowregistry"
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
	"github.com/monbooru/monbooru/internal/acquisitionstage"
	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/jobs"
	"github.com/monbooru/monbooru/internal/library"
)

// Fixed disposable library host; no scanner, normal HTTP API or real
// profile. The same library Set, database, allocator and commit code
// used by the managed candidate are exercised.
type windowHost struct{cfg config.Gallery}
func(h windowHost)Layout()library.Layout{return library.Layout{Galleries:[]config.Gallery{h.cfg},DataPath:filepath.Dir(h.cfg.DBPath)}}
func(h windowHost)Background(string)library.Background{return library.Background{}}
func(h windowHost)DefaultGallery()string{return h.cfg.Name}
func(h windowHost)Added(config.Gallery)error{return managedqa.ErrFixture}
func(h windowHost)Removed(string)error{return managedqa.ErrFixture}
func(h windowHost)Repointed(string,string)error{return managedqa.ErrFixture}
func(h windowHost)Renamed(string,string,string,string)error{return managedqa.ErrFixture}

type windowFixtureOwner struct{
	mu sync.Mutex
	dir string
	setup managedqa.Setup
	set *library.Set
	a *acquisitionstage.Allocator
	lease *acquisitionpolicy.OnlineLease
	registry *windowregistry.Registry
	window windowregistry.Grant
	generation string
	allowed map[string]workerproto.StageAllocateRequest
	grant workerproto.StageAllocation
	manifest,projection []byte
	hash string
	result workerproto.WindowOutcome
}

func(o *windowFixtureOwner) current(c context.Context)bool{return c.Err()==nil&&o.lease!=nil&&o.lease.Current()}
func(o *windowFixtureOwner) scope(c context.Context,q workerproto.StageAllocateRequest)(acquisitionstage.Scope,error){
	if !o.current(c)||o.allowed[q.OperationID]!=q{return acquisitionstage.Scope{},managedqa.ErrFixture}
	root:=filepath.Join(o.dir,"gallery")
	s:=acquisitionstage.Scope{GalleryIdentity:"disposable-window:"+o.dir,GalleryRoot:root,GalleryRoots:[]string{root},MaxFiles:1,MaxBytes:128<<10}
	if q.OperationID==managedqa.ParentID(q.Transfer.Metadata.JobID){s.MaxFiles=uint32(o.setup.Count);s.MaxBytes=8<<20}
	return s,nil
}

// Independent fixture authority: reconstruct the allowed manifest from
// actual native metadata observed by the custodian after cleanup. The
// known synthetic source has exactly one blocked item and otherwise
// safe fixture-tagged images. Refuse any widened or different claims.
func(o *windowFixtureOwner) authorizeProjection(c context.Context,p workerproto.WindowAllocateProposal)([]byte,error){
	if !o.current(c)||p.Transfer.Metadata!=o.window.Request||p.Transfer.Subcategory!="search"||p.Transfer.PolicyGeneration!=1{return nil,managedqa.ErrFixture}
	raw,err:=managedqa.Read(o.dir,managedqa.JobFile(managedqa.MetadataName,o.window.Request.JobID),workerproto.MaxMetadataOutputBytes);if err!=nil{return nil,err}
	posts,err:=workerproto.DecodeMetadataEvidence(raw,o.window.Request,"search")
	if err!=nil||len(posts)!=o.setup.Count{return nil,managedqa.ErrFixture}
	ids:=o.setup.ExpectedIDs(o.window.Request.JobID)
	for i,post:=range posts{
		id:=strconv.Itoa(o.setup.Offset(o.window.Request.JobID)+i+1)
		if post.ID!=id||post.MediaURL!=o.setup.MediaURL(id){return nil,managedqa.ErrFixture}
		var native struct{Tags []string `json:"tags"`;TagIDs []int `json:"tag_ids"`}
		if json.Unmarshal(post.Native,&native)!=nil{return nil,managedqa.ErrFixture}
		tags:=[]string{"safe","fixture"};tagIDs:=[]int{1,4}
		if i==0{tags=[]string{"safe","violence"};tagIDs=[]int{1,3}}
		if !slices.Equal(native.Tags,tags)||!slices.Equal(native.TagIDs,tagIDs){return nil,managedqa.ErrFixture}
	}
	_,cfg,identity:=o.setup.Identity()
	plan,err:=acquisitionplan.NewStrictTransferPlan(o.window.Request,"search",posts,ids,1,identity,cfg)
	if err!=nil||plan.Request()!=p.Transfer{return nil,managedqa.ErrFixture}
	manifest,err:=plan.Consume(p.Transfer,identity,1);if err!=nil{return nil,err}
	snapshot,err:=acquisitionplan.DecodeApprovedPolicy(p.Projection,p.Transfer,p.PolicySHA256,manifest)
	if err!=nil||len(snapshot.Rows)!=len(ids){return nil,managedqa.ErrFixture}
	for i,row:=range snapshot.Rows{
		id:=ids[i];native:=posts[i+1]
		if row.PostID!=id||row.NativeSHA256!=managedqa.Digest(native.Native)||row.MediaURL!=native.MediaURL||
			!strings.EqualFold(row.SourceLabel,"derpibooru")||row.PostURL!="https://derpibooru.org/images/"+id||row.Rating!="general"||
			len(row.Tags)!=2||!slices.Contains(row.Tags,"fixture")||!slices.Contains(row.Tags,"rating:general")||
			len(row.WithheldTags)!=0||len(row.WithheldCategories)!=0||len(row.UnknownTargets)!=0||row.Provenance==nil{return nil,managedqa.ErrFixture}
		v:=row.Provenance
		if v.ParentURL!=""||v.Collection!=""||v.SourceMD5!=""||v.Original!=""||v.Commentary!=""||v.CommentaryDText!=""||v.CommentaryTranslated!=""||v.CommentaryTranslatedDText!=""||len(v.Notes)!=0{return nil,managedqa.ErrFixture}
	}
	return manifest,nil
}
func(o *windowFixtureOwner) handle(c context.Context,op workerproto.Operation,raw json.RawMessage)(json.RawMessage,*workerproto.Fault){
	o.mu.Lock();defer o.mu.Unlock()
	fault:=func(err error)(json.RawMessage,*workerproto.Fault){fmt.Fprintln(os.Stderr,"disposable owner:",op,err);return nil,&workerproto.Fault{Code:"refused"}}
	if !o.current(c)||o.registry==nil||!o.registry.Current(o.window){return fault(managedqa.ErrFixture)}
	switch op{
	case workerproto.StageAllocate:
		if o.grant!=(workerproto.StageAllocation{}){return fault(managedqa.ErrFixture)}
		p,err:=workerproto.DecodeWindowAllocateProposal(raw);if err!=nil{return fault(err)}
		manifest,err:=o.authorizeProjection(c,p);if err!=nil{return fault(err)}
		q:=workerproto.StageAllocateRequest{OperationID:managedqa.ParentID(q.Transfer.Metadata.JobID),Transfer:p.Transfer}
		o.allowed[q.OperationID]=q
		g,err:=o.a.Allocate(c,q);if err!=nil{return fault(err)}
		o.grant,o.manifest,o.projection,o.hash=g,manifest,bytes.Clone(p.Projection),p.PolicySHA256
		if err:=managedqa.WriteJSON(o.dir,managedqa.JobFile(managedqa.GrantName,o.window.Request.JobID),managedqa.Grant{Allocation:g,PolicySHA256:o.hash});err!=nil{return fault(err)}
		body,err:=workerproto.EncodeStageAllocation(g,q);if err!=nil{return fault(err)};return body,nil
	case workerproto.ImportMedia:
		p,err:=workerproto.DecodeWindowCommitRequest(raw)
		if err!=nil||p.Allocation!=o.grant||p.PolicySHA256!=o.hash||len(o.result.Items)!=0{return fault(managedqa.ErrFixture)}
		if err:=o.commit(c);err!=nil{return fault(err)}
		body,err:=workerproto.EncodeWindowOutcome(o.result,o.grant,o.hash);if err!=nil{return fault(err)};return body,nil
	case workerproto.FetchOutcome:
		p,err:=workerproto.DecodeWindowCommitRequest(raw)
		if err!=nil||p.Allocation!=o.grant||p.PolicySHA256!=o.hash{return fault(managedqa.ErrFixture)}
		v,err:=o.a.ObserveWindowOutcome(c,p.Allocation,p.PolicySHA256);if err!=nil{return fault(err)}
		body,err:=workerproto.EncodeWindowOutcome(v,p.Allocation,p.PolicySHA256);if err!=nil{return fault(err)};return body,nil
	default:return fault(managedqa.ErrFixture)
	}
}
func(o *windowFixtureOwner) commit(c context.Context)error{
	current:=func(c context.Context,g workerproto.StageAllocation)error{
		if !o.current(c)||o.allowed[g.Request.OperationID]!=g.Request{return managedqa.ErrFixture};return nil
	}
	// The custodian writes this exact proof only after contained Stop
	// and final file verification. The owner also checks sealed bytes.
	proof:=func(c context.Context,g workerproto.StageAllocation)([]byte,[]acquisitionplan.VerifiedTransferFile,error){
		var p managedqa.Proof
		if current(c,g)!=nil||g!=o.grant||managedqa.ReadJSON(o.dir,managedqa.JobFile(managedqa.ProofName,o.window.Request.JobID),&p,1<<20)!=nil||p.Allocation!=g||len(p.Files)!=o.setup.Count-1{return nil,nil,managedqa.ErrFixture}
		return o.manifest,p.Files,nil
	}
	cleanup:=func(c context.Context,g workerproto.StageAllocation,_ acquisitionstage.OwnerReceipt)error{return current(c,g)}
	reclaim:=func(c context.Context,g workerproto.StageAllocation,_ acquisitionstage.BatchOutcome)error{return current(c,g)}
	owner:=acquisitionstage.OwnedWindow{
		Authorize:current,ConfirmStopped:proof,
		Policy:func(c context.Context,g workerproto.StageAllocation)([]byte,string,error){if current(c,g)!=nil{return nil,"",managedqa.ErrFixture};return o.projection,o.hash,nil},
		Batch:acquisitionstage.BatchOwner{
			Current:current,Archive:cleanup,
			Child:func(c context.Context,parent workerproto.StageAllocation,q workerproto.StageAllocateRequest,row acquisitionplan.ApprovedPolicyRow)(acquisitionstage.OwnerCommit,error){
				if current(c,parent)!=nil||q.Transfer.Metadata!=o.window.Request{return acquisitionstage.OwnerCommit{},managedqa.ErrFixture}
				var receipt managedqa.Proof
				if err:=managedqa.ReadJSON(o.dir,managedqa.JobFile(managedqa.ProofName,o.window.Request.JobID),&receipt,1<<20);err!=nil{return acquisitionstage.OwnerCommit{},err}
				var file acquisitionplan.VerifiedTransferFile
				for _,f:=range receipt.Files{if f.ID==row.PostID{file=f;break}}
				disposition,err:=acquisitionstage.ResolveOwnerDisposition(c,o.set,managedqa.Gallery,file,row)
				if err!=nil{return acquisitionstage.OwnerCommit{},err}
				o.allowed[q.OperationID]=q
				return acquisitionstage.OwnerCommit{
					Current:current,
					Disposition:func(c context.Context,g workerproto.StageAllocation)(string,error){return disposition,current(c,g)},
					Naming:func(c context.Context,g workerproto.StageAllocation)(gallery.ReceivedNamingRequest,error){return gallery.ReceivedNamingRequest{Gallery:managedqa.Gallery,ReceivedName:row.PostID+".png",Source:row.SourceLabel,PostID:row.PostID,DefaultName:"{source}-{post_id}"},current(c,g)},
					Extras:func(c context.Context,g workerproto.StageAllocation)(contract.JobExtras,error){return contract.JobExtras{},current(c,g)},
					Cleanup:cleanup,
				},nil
			},
		},Reclaim:reclaim,
	}
	if _,err:=o.a.CompleteTransferredForOwner(c,o.lease,o.set,o.grant,owner);err!=nil{return err}
	if _,err:=o.a.ArchiveBatch(c,o.grant,reclaim);err!=nil{return err}
	v,err:=o.a.ObserveWindowOutcome(c,o.grant,o.hash);if err!=nil{return err}
	if err:=o.registry.RecordOutcome(c,windowregistry.Outcome{Grant:o.window,Kind:"committed",PolicySHA256:v.PolicySHA256,PlanSHA256:v.PlanSHA256});err!=nil{return err}
	o.result=v
	return nil
}

func runManagedWindowProbe(countArg string)(result error){
	windows:=1
	if countArg=="3x2"{windows=2;countArg="3"}
	count,err:=strconv.Atoi(countArg);if err!=nil||count!=3&&count!=200{return managedqa.ErrFixture}
	python:=os.Getenv("MEBOORU_METADATA_TEST_PYTHON")
	if !filepath.IsAbs(python){return managedqa.ErrFixture}
	if _,err:=processguard.ProbeSibling("monbooru","monloader");err!=nil{return err}
	self,err:=os.Executable();if err!=nil{return err}
	self,err=filepath.EvalSymlinks(self);if err!=nil{return err}
	temp,err:=filepath.EvalSymlinks(os.TempDir());if err!=nil{return err}
	dir,err:=os.MkdirTemp(temp,managedqa.Prefix);if err!=nil{return err}
	safe:=false
	defer func(){if safe{result=errors.Join(result,os.RemoveAll(dir))}else{fmt.Fprintln(os.Stderr,"retained disposable window:",dir)}}()
	for _,name:=range []string{"stage","gallery","queue","windows"}{if err:=os.Mkdir(filepath.Join(dir,name),0700);err!=nil{return err}}
	ctx,cancel:=context.WithTimeout(context.Background(),10*time.Minute);defer cancel()
	ln,err:=net.Listen("tcp4","127.0.0.1:0");if err!=nil{return err}
	var requestMu sync.Mutex
	requests:=map[int]int{}
	imageBytes:=map[int][]byte{}
	for id:=1;id<=count*windows;id++{
		var b bytes.Buffer
		img:=image.NewRGBA(image.Rect(0,0,16,16))
		img.Set(0,0,color.RGBA{R:uint8(id),A:255})
		if err:=png.Encode(&b,img);err!=nil{ln.Close();return err}
		imageBytes[id]=b.Bytes()
	}
	server:=&http.Server{ReadHeaderTimeout:5*time.Second,Handler:http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		id,err:=strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path,"/"),".png"))
		if err!=nil||r.Method!="GET"||r.URL.Path!="/"+strconv.Itoa(id)+".png"||imageBytes[id]==nil{http.NotFound(w,r);return}
		requestMu.Lock();requests[id]++;requestMu.Unlock()
		w.Header().Set("Content-Type","image/png");_,_=w.Write(imageBytes[id])
	})}
	defer server.Close();go server.Serve(ln)
	setup:=managedqa.Setup{Count:count,Windows:windows,Port:ln.Addr().(*net.TCPAddr).Port,Python:python,ScriptSHA256:managedqa.Digest([]byte(managedqa.PythonFixture))}
	if err:=managedqa.Write(dir,managedqa.ScriptName,[]byte(managedqa.PythonFixture));err!=nil{return err}
	if err:=managedqa.WriteJSON(dir,managedqa.SetupName,setup);err!=nil{return err}
	cfg:=config.Gallery{Name:managedqa.Gallery,GalleryPath:filepath.Join(dir,"gallery"),DBPath:filepath.Join(dir,"gallery.sqlite"),ThumbnailsPath:filepath.Join(dir,"thumbs")}
	set,err:=library.OpenSet(windowHost{cfg},jobs.NewManager(),[]config.Gallery{cfg},cfg.Name);if err!=nil{return err}
	defer set.Close()
	registry,err:=windowregistry.Open(filepath.Join(dir,"windows"));if err!=nil{return err};defer registry.Close()
	o:=&windowFixtureOwner{dir:dir,setup:setup,set:set,allowed:map[string]workerproto.StageAllocateRequest{},registry:registry}
	a,err:=acquisitionstage.Open(filepath.Join(dir,"stage"),2,32<<20,o.scope);if err!=nil{return err}
	o.a=a;defer a.Close()
	lock,err:=acquisitionpolicy.LockProfile(dir);if err!=nil{return err};defer lock.Close()
	store,err:=acquisitionpolicy.Open(dir);if err!=nil{return err};defer store.Close()
	policy,err:=acquisitionpolicy.New(store);if err!=nil{return err}
	manager,err:=acquisitionpolicy.NewManager(ctx,lock,policy,func(start,lifetime context.Context,f *acquisitionpolicy.WorkerFence)(acquisitionpolicy.ManagedPeer,error){
		o.mu.Lock();o.generation=f.Generation();o.mu.Unlock()
		cmd:=exec.Command(self,"internal-managed-window-custody-v1",dir,f.Generation())
		cmd.Stderr=os.Stderr
		return acquisitionpolicy.StartCustodySession(start,lifetime,cmd,f,o.handle)
	},acquisitionpolicy.LifecycleOptions{IdleAfter:time.Millisecond,RestartDelay:time.Millisecond})
	if err!=nil{return err}
	defer func(){stop,cancel:=context.WithTimeout(context.Background(),25*time.Second);defer cancel();if err:=manager.Shutdown(stop);err!=nil{safe=false;result=errors.Join(result,err)}}()
	local,err:=manager.Acquire(ctx);if err!=nil{return err};defer local.Release()
	peer:=local.Peer.(*acquisitionpolicy.CustodySessionPeer)
	s:=peer.Session()
	call:=func(op workerproto.Operation,body json.RawMessage)(json.RawMessage,error){
		e,err:=s.Call(ctx,op,body);if err!=nil{return nil,err};if e.Error!=nil{return nil,fmt.Errorf("%s: %s",op,e.Error.Code)};return e.Payload,nil
	}
	type jobView struct{
		Status string `json:"status"`
		Error string `json:"error"`
		Items []struct{PostID string `json:"post_id"`;Outcome string `json:"outcome"`;ImageID int64 `json:"monbooru_id"`;Receipt string `json:"managed_receipt_sha256"`} `json:"items"`
		ManagedWindow *struct{State string `json:"state"`} `json:"managed_window"`
	}
	var lease *acquisitionpolicy.OnlineLease
	for job:=int64(1);job<=int64(windows);job++{
		md:=setup.MetadataFor(job)
		if _,err:=call(workerproto.QueuePause,nil);err!=nil{return err}
		body,err:=workerproto.EncodeQueueCommand(workerproto.QueueEnqueue,workerproto.QueueCommand{URL:md.URL,Gallery:md.Gallery,MaxItems:count});if err!=nil{return err}
		raw,err:=call(workerproto.QueueEnqueue,body);if err!=nil{return err}
		var accepted struct{JobID int64 `json:"job_id"`}
		if json.Unmarshal(raw,&accepted)!=nil||accepted.JobID!=job{return managedqa.ErrFixture}
		get,err:=workerproto.EncodeQueueCommand(workerproto.QueueGet,workerproto.QueueCommand{JobID:job});if err!=nil{return err}
		readJob:=func()(jobView,error){
			var v jobView
			raw,err:=call(workerproto.QueueGet,get);if err!=nil{return v,err}
			err=json.Unmarshal(raw,&v);return v,err
		}
		v,err:=readJob();if err!=nil||v.Status!="queued"{return fmt.Errorf("parked queue: %v %v",v.Status,err)}
		if job==1{
			requestMu.Lock();none:=len(requests)==0;requestMu.Unlock()
			if !none{return managedqa.ErrFixture}
			demand,err:=acquisitionpolicy.ReadQueueDemand(ctx,filepath.Join(dir,"queue"),time.Now(),false)
			if err!=nil||!demand.Queued{return fmt.Errorf("durable queue demand: %v",err)}
			if err:=manager.TickNetworkDemand(ctx,demand.Queued);err!=nil{return err}
			if err:=manager.SetOffline(ctx,false);err!=nil{return err}
			if err:=manager.TickNetworkDemand(ctx,demand.Queued);err!=nil{return err}
			lease,err=manager.AcquireOnline(ctx);if err!=nil{return err}
			defer lease.Release()
		}
		o.mu.Lock()
		o.lease=lease
		o.window=windowregistry.Grant{Request:md,OwnerGeneration:o.generation,PolicyGeneration:1}
		o.grant=workerproto.StageAllocation{}
		o.result=workerproto.WindowOutcome{}
		o.manifest,o.projection,o.hash=nil,nil,""
		o.mu.Unlock()
		if err:=registry.Register(ctx,o.window);err!=nil{return err}
		if _,err:=call(workerproto.QueueResume,nil);err!=nil{return err}
		for{
			v,err=readJob();if err!=nil{return err}
			if v.Status=="succeeded"{break}
			if v.Status!="queued"&&v.Status!="running"{return fmt.Errorf("managed job %s: %s",v.Status,v.Error)}
			select{case <-ctx.Done():return ctx.Err();case <-time.After(200*time.Millisecond):}
		}
		if registry.Current(o.window)||v.ManagedWindow==nil||v.ManagedWindow.State!="complete"||len(v.Items)!=count{return managedqa.ErrFixture}
		offset:=setup.Offset(job)
		if v.Items[0].PostID!=strconv.Itoa(offset+1)||v.Items[0].Outcome!=string(contract.OutcomeSkippedBlacklist){return managedqa.ErrFixture}
		for i,it:=range v.Items[1:]{
			if it.PostID!=strconv.Itoa(offset+i+2)||it.Outcome!=string(contract.OutcomeCreated)||it.ImageID<=0||len(it.Receipt)!=64{return managedqa.ErrFixture}
		}
		o.mu.Lock();observed:=o.result;o.mu.Unlock()
		if len(observed.Items)!=count-1{return managedqa.ErrFixture}
		g:=set.Get(cfg.Name)
		var n int
		if err:=g.DB.Read.QueryRowContext(ctx,"SELECT COUNT(*) FROM images WHERE is_missing=0").Scan(&n);err!=nil||n!=int(job)*(count-1){return fmt.Errorf("library rows %d: %v",n,err)}
		for _,it:=range observed.Items{
			id,err:=strconv.Atoi(it.PostID);if err!=nil||it.State!="committed"||it.SHA256!=managedqa.Digest(imageBytes[id]){return managedqa.ErrFixture}
			var path string
			if err:=g.DB.Read.QueryRowContext(ctx,"SELECT canonical_path FROM images WHERE id=?",it.ImageID).Scan(&path);err!=nil{return err}
			got,err:=os.ReadFile(path);if err!=nil||!bytes.Equal(got,imageBytes[id]){return managedqa.ErrFixture}
		}
		requestMu.Lock()
		exact:=len(requests)==int(job)*(count-1)&&requests[offset+1]==0
		for i:=offset+2;i<=offset+count;i++{exact=exact&&requests[i]==1}
		requestMu.Unlock()
		if !exact{return managedqa.ErrFixture}
	}
	// Persist revocation before requesting drain. Old grant cannot be
	// revived by any future transition in this single-window fixture.
	if err:=managedqa.Write(dir,managedqa.RevokedName,[]byte("revoked"));err!=nil{return err}
	if err:=manager.SetOffline(ctx,true);err!=nil{return err}
	lease.Release();local.Release()
	if err:=manager.Shutdown(ctx);err!=nil{return err}
	if offline,err:=store.Load();err!=nil||!offline{return managedqa.ErrFixture}
	safe=true
	if windows==1{fmt.Printf("PASS: disposable managed executable window %d listed, %d committed, one blocked, exact receipts and cleanup\n",count,count-1)}else{fmt.Println("PASS: two successive managed executable windows on one worker session, exact durable settlement")}
	return nil
}
