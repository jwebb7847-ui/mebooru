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
	"github.com/jwebb7847-ui/mebooru/shared/logx"
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

type managedWindowOwner struct{
	mu sync.Mutex
	dir string
	setup managedqa.Setup
	live *windowregistry.LiveConfig
	set *library.Set
	naming config.GalleryConfig
	a *acquisitionstage.Allocator
	lease *acquisitionpolicy.OnlineLease
	bindOnline func() (*acquisitionpolicy.OnlineLease, error)
	registry *windowregistry.Registry
	window windowregistry.Grant
	generation string
	allowed map[string]workerproto.StageAllocateRequest
	grant workerproto.StageAllocation
	cleaning bool
	manifest,projection []byte
	hash string
	result workerproto.WindowOutcome
}

func(o *managedWindowOwner) current(c context.Context)bool{return c.Err()==nil&&o.lease!=nil&&o.lease.Current()}
func (o *managedWindowOwner) galleryIdentity(name string) string {
	g := o.set.Get(name)
	if g == nil || g.Degraded || g.Closed() {
		return ""
	}
	return managedqa.Digest([]byte(g.Name + "\x00" + g.DBPath + "\x00" + g.GalleryPath))
}

func(o *managedWindowOwner) scope(c context.Context,q workerproto.StageAllocateRequest)(acquisitionstage.Scope,error){
	if (!o.cleaning&&!o.current(c))||q.Transfer.Metadata!=o.window.Request||o.allowed[q.OperationID]!=q{return acquisitionstage.Scope{},managedqa.ErrFixture}
	g := o.set.Get(q.Transfer.Metadata.Gallery)
	if g == nil || o.galleryIdentity(g.Name) != o.window.GalleryIdentity {
		return acquisitionstage.Scope{}, managedqa.ErrFixture
	}
	members := o.set.All()
	roots := make([]string, 0, len(members))
	for _, member := range members { roots = append(roots, member.GalleryPath) }
	s:=acquisitionstage.Scope{GalleryIdentity:o.window.GalleryIdentity,GalleryRoot:g.GalleryPath,GalleryRoots:roots,MaxFiles:1,MaxBytes:128<<10}
	if o.live!=nil{
		s.MaxBytes=64<<20
		if q.OperationID==managedqa.ParentID(q.Transfer.Metadata.JobID){
			s.MaxFiles=q.Transfer.Metadata.Last-q.Transfer.Metadata.First+1
			s.MaxBytes=min(uint64(s.MaxFiles)*64<<20,workerproto.MaxStageTotalBytes)
		}
		return s,nil
	}
	if q.OperationID==managedqa.ParentID(q.Transfer.Metadata.JobID){s.MaxFiles=uint32(o.setup.Count);s.MaxBytes=8<<20}
	return s,nil
}

// Independent fixture authority: reconstruct the allowed manifest from
// actual native metadata observed by the custodian after cleanup. The
// known synthetic source has exactly one blocked item and otherwise
// safe fixture-tagged images. Refuse any widened or different claims.
func(o *managedWindowOwner) fixtureListing(c context.Context)([]workerproto.MetadataPost,error){
	if !o.current(c){return nil,managedqa.ErrFixture}
	raw,err:=managedqa.Read(o.dir,managedqa.JobFile(managedqa.MetadataName,o.window.Request.JobID),workerproto.MaxMetadataOutputBytes);if err!=nil{return nil,err}
	posts,err:=workerproto.DecodeMetadataEvidence(raw,o.window.Request,"search")
	if err!=nil||len(posts)!=o.setup.Count{return nil,managedqa.ErrFixture}
	for i,post:=range posts{
		id:=strconv.Itoa(o.setup.Offset(o.window.Request.JobID)+i+1)
		if post.ID!=id||post.MediaURL!=o.setup.MediaURL(id){return nil,managedqa.ErrFixture}
		var native struct{Tags []string `json:"tags"`;TagIDs []int `json:"tag_ids"`}
		if json.Unmarshal(post.Native,&native)!=nil{return nil,managedqa.ErrFixture}
		tags:=[]string{"safe","fixture"};tagIDs:=[]int{1,4}
		if o.setup.Blocked(o.window.Request.JobID,i){tags=[]string{"safe","violence"};tagIDs=[]int{1,3}}
		if !slices.Equal(native.Tags,tags)||!slices.Equal(native.TagIDs,tagIDs){return nil,managedqa.ErrFixture}
	}
	return posts,nil
}
func(o *managedWindowOwner) authorizeProjection(c context.Context,p workerproto.WindowAllocateProposal)([]byte,error){
	if o.live!=nil{return o.authorizeLiveProjection(c,p)}
	if !o.current(c)||p.Transfer.Metadata!=o.window.Request||p.Transfer.Subcategory!="search"||p.Transfer.PolicyGeneration!=1{return nil,managedqa.ErrFixture}
	posts,err:=o.fixtureListing(c);if err!=nil{return nil,err}
	ids:=o.setup.ExpectedIDs(o.window.Request.JobID);if len(ids)==0{return nil,managedqa.ErrFixture}
	_,cfg,identity:=o.setup.Identity()
	plan,err:=acquisitionplan.NewStrictTransferPlan(o.window.Request,"search",posts,ids,1,identity,cfg)
	if err!=nil||plan.Request()!=p.Transfer{return nil,managedqa.ErrFixture}
	manifest,err:=plan.Consume(p.Transfer,identity,1);if err!=nil{return nil,err}
	snapshot,err:=acquisitionplan.DecodeApprovedPolicy(p.Projection,p.Transfer,p.PolicySHA256,manifest)
	if err!=nil||len(snapshot.Rows)!=len(ids){return nil,managedqa.ErrFixture}
	for i,row:=range snapshot.Rows{
		id:=ids[i];native:=posts[len(posts)-len(ids)+i]
		if row.PostID!=id||row.NativeSHA256!=managedqa.Digest(native.Native)||row.MediaURL!=native.MediaURL||
			!strings.EqualFold(row.SourceLabel,"derpibooru")||row.PostURL!="https://derpibooru.org/images/"+id||row.Rating!="general"||
			len(row.Tags)!=2||!slices.Contains(row.Tags,"fixture")||!slices.Contains(row.Tags,"rating:general")||
			len(row.WithheldTags)!=0||len(row.WithheldCategories)!=0||len(row.UnknownTargets)!=0||row.Provenance==nil{return nil,managedqa.ErrFixture}
		v:=row.Provenance
		if v.ParentURL!=""||v.Collection!=""||v.SourceMD5!=""||v.Original!=""||v.Commentary!=""||v.CommentaryDText!=""||v.CommentaryTranslated!=""||v.CommentaryTranslatedDText!=""||len(v.Notes)!=0{return nil,managedqa.ErrFixture}
	}
	return manifest,nil
}
func(o *managedWindowOwner) handle(c context.Context,op workerproto.Operation,raw json.RawMessage)(json.RawMessage,*workerproto.Fault){
	o.mu.Lock();defer o.mu.Unlock()
	fault:=func(err error)(json.RawMessage,*workerproto.Fault){fmt.Fprintln(os.Stderr,"acquisition owner:",op,err);return nil,&workerproto.Fault{Code:"refused"}}
	if o.registry==nil{return fault(managedqa.ErrFixture)}
	if op==workerproto.WindowPrepare{
		p,err:=workerproto.DecodeWindowPrepare(raw)
		if err!=nil||o.live==nil&&p.Request!=o.setup.MetadataFor(p.Request.JobID){return fault(managedqa.ErrFixture)}
		if o.window!=(windowregistry.Grant{}){
			if _,err:=o.registry.ObserveSettlement(o.window);err!=nil{return fault(err)}
		}
		sub,err:=o.registry.ObserveSubmission(p.IntentID)
		if err!=nil||sub.GalleryIdentity!=o.galleryIdentity(p.Request.Gallery){return fault(managedqa.ErrFixture)}
		if o.bindOnline != nil {
			o.lease.Release()
			o.lease, err = o.bindOnline()
			if err != nil { return fault(err) }
		}
		_,_,identity:=o.setup.Identity()
		if o.live!=nil{_,identity=o.live.Identity(p.Request.Source)}
		g:=windowregistry.Grant{Request:p.Request,OwnerGeneration:o.generation,PolicyGeneration:1,GalleryIdentity:sub.GalleryIdentity,Identity:identity,IntentID:p.IntentID}
		if err:=acquisitionpolicy.RegisterRunningIntentWindow(c,filepath.Join(o.dir,"queue"),o.registry,g,func(c context.Context,v windowregistry.Grant)bool{return o.current(c)&&v==g});err!=nil{return fault(err)}
		o.window=g
		o.grant=workerproto.StageAllocation{}
		o.result=workerproto.WindowOutcome{}
		o.manifest,o.projection,o.hash=nil,nil,""
		body,_:=workerproto.EncodeWindowPrepare(p);return body,nil
	}
	if op==workerproto.StageAbandon && o.live!=nil {
		q,err:=workerproto.DecodeStageAbandonRequest(raw)
		if err!=nil||q.OperationID!=o.grant.Request.OperationID||q.TransferID!=o.grant.TransferID{return fault(windowregistry.ErrWindow)}
		out,err:=o.registry.ObserveSettlement(o.window)
		if err!=nil||out.Kind!="abandoned"{return fault(windowregistry.ErrWindow)}
		o.cleaning=true
		defer func(){o.cleaning=false}()
		if _,err:=o.a.AbandonPrepublication(c,q,func(_ context.Context,a workerproto.StageAllocation)error{
			if a!=o.grant{return windowregistry.ErrWindow}
			return nil
		});err!=nil{return fault(err)}
		return raw,nil
	}
	if !o.current(c)||!o.registry.Current(o.window){return fault(managedqa.ErrFixture)}
	if op!=workerproto.FetchOutcome&&!o.registry.Admitting(o.window){return fault(managedqa.ErrFixture)}
	switch op{
	case workerproto.WindowNoTransfer:
		p,err:=workerproto.DecodeWindowNoTransfer(raw)
		if err!=nil||p.Request!=o.window.Request||o.grant!=(workerproto.StageAllocation{}){return fault(managedqa.ErrFixture)}
		if o.live!=nil{
			pol,_,err:=o.livePolicy()
			if err!=nil||len(pol.Proposal.Projection)!=0||!slices.Equal(pol.Listed,p.ListedIDs)||!slices.Equal(pol.Blocked,p.BlockedIDs){return fault(windowregistry.ErrWindow)}
		}else{
			if !o.setup.AllBlocked(p.Request.JobID){return fault(managedqa.ErrFixture)}
			posts,err:=o.fixtureListing(c);if err!=nil||len(posts)!=len(p.ListedIDs){return fault(managedqa.ErrFixture)}
			for i,v:=range posts{if v.ID!=p.ListedIDs[i]{return fault(managedqa.ErrFixture)}}
		}
		if err:=o.registry.RecordOutcome(c,windowregistry.Outcome{Grant:o.window,Kind:"no_transfer"});err!=nil{return fault(err)}
		body,_:=json.Marshal(workerproto.ManagedMetadataStart{Request:p.Request});return body,nil
	case workerproto.StageAllocate:
		if o.grant!=(workerproto.StageAllocation{}){return fault(managedqa.ErrFixture)}
		p,err:=workerproto.DecodeWindowAllocateProposal(raw);if err!=nil{return fault(err)}
		manifest,err:=o.authorizeProjection(c,p);if err!=nil{return fault(err)}
		q:=workerproto.StageAllocateRequest{OperationID:managedqa.ParentID(p.Transfer.Metadata.JobID),Transfer:p.Transfer}
		o.allowed[q.OperationID]=q
		g,err:=o.a.Allocate(c,q);if err!=nil{return fault(err)}
		o.grant,o.manifest,o.projection,o.hash=g,manifest,bytes.Clone(p.Projection),p.PolicySHA256
		if o.live!=nil{
			err=windowregistry.WriteArtifact(o.dir,windowregistry.ArtifactName(o.window.Request.JobID,"stage"),windowregistry.TransferGrant{Allocation:g,PolicySHA256:o.hash},64<<10)
		}else{
			err=managedqa.WriteJSON(o.dir,managedqa.JobFile(managedqa.GrantName,o.window.Request.JobID),managedqa.Grant{Allocation:g,PolicySHA256:o.hash})
		}
		if err!=nil{return fault(err)}
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
func(o *managedWindowOwner) commit(c context.Context)error{
	if o.live!=nil{
		resume,err:=o.set.PauseAcquisitionWatcher(o.window.Request.Gallery)
		if err!=nil{return err}
		defer resume()
	}
	current:=func(c context.Context,g workerproto.StageAllocation)error{
		if !o.current(c)||o.allowed[g.Request.OperationID]!=g.Request{return managedqa.ErrFixture};return nil
	}
	// The custodian writes this exact proof only after contained Stop
	// and final file verification. The owner also checks sealed bytes.
	filesByID := make(map[string]acquisitionplan.VerifiedTransferFile)
	proof:=func(c context.Context,g workerproto.StageAllocation)([]byte,[]acquisitionplan.VerifiedTransferFile,error){
		var p managedqa.Proof
		if current(c,g)!=nil||g!=o.grant{return nil,nil,windowregistry.ErrWindow}
		if o.live!=nil{
			if err:=windowregistry.ReadArtifact(o.dir,windowregistry.ArtifactName(o.window.Request.JobID,"transfer"),&p,1<<20);err!=nil{return nil,nil,err}
		}else if managedqa.ReadJSON(o.dir,managedqa.JobFile(managedqa.ProofName,o.window.Request.JobID),&p,1<<20)!=nil||len(p.Files)!=len(o.setup.ExpectedIDs(o.window.Request.JobID)){return nil,nil,managedqa.ErrFixture}
		if p.Allocation!=g{return nil,nil,windowregistry.ErrWindow}
		for _, file := range p.Files { filesByID[file.ID] = file }
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
				file, ok := filesByID[row.PostID]
				if !ok { return acquisitionstage.OwnerCommit{}, managedqa.ErrFixture }
				destination := parent.Request.Transfer.Metadata.Gallery
				disposition,err:=acquisitionstage.ResolveOwnerDisposition(c,o.set,destination,file,row)
				if err!=nil{return acquisitionstage.OwnerCommit{},err}
				o.allowed[q.OperationID]=q
				return acquisitionstage.OwnerCommit{
					Current:current,
					Disposition:func(c context.Context,g workerproto.StageAllocation)(string,error){return disposition,current(c,g)},
					Naming:func(c context.Context,g workerproto.StageAllocation)(gallery.ReceivedNamingRequest,error){return gallery.ReceivedNamingRequest{Gallery:destination,ReceivedName:file.RelativeName,Source:row.SourceLabel,PostID:row.PostID,DefaultFolder:o.naming.DefaultUploadFolder,DefaultName:o.naming.DefaultUploadName},current(c,g)},
					Extras:func(c context.Context,g workerproto.StageAllocation)(contract.JobExtras,error){return contract.JobExtras{},current(c,g)},
					Cleanup:cleanup,
				},nil
			},
		},Reclaim:reclaim,
	}
	if _,err:=o.a.CompleteTransferredForOwner(c,o.lease,o.set,o.grant,owner);err!=nil{return err}
	settlement:=time.Now()
	if _,err:=o.a.ArchiveBatch(c,o.grant,reclaim);err!=nil{return err}
	v,err:=o.a.ObserveWindowOutcome(c,o.grant,o.hash);if err!=nil{return err}
	if err:=o.registry.RecordOutcome(c,windowregistry.Outcome{Grant:o.window,Kind:"committed",PolicySHA256:v.PolicySHA256,PlanSHA256:v.PlanSHA256});err!=nil{return err}
	o.result=v
	logx.Infof("managed acquisition job=%d stage=settlement duration=%s",o.window.Request.JobID,time.Since(settlement).Round(time.Millisecond))
	return nil
}

func runManagedWindowProbe(countArg string)(result error){
	windows:=1
	cancelFirst := countArg == "app-cancel"
	if cancelFirst { countArg = "app-3" }
	app := strings.HasPrefix(countArg, "app-")
	if app {
		countArg = strings.TrimPrefix(countArg, "app-")
		if countArg == "3" { windows = 2 }
	}
	if countArg=="3x2"{windows=2;countArg="3"}else if countArg=="3x3"{windows=3;countArg="3"}
	count,err:=strconv.Atoi(countArg);if err!=nil||!managedqa.ValidCount(count,app){return managedqa.ErrFixture}
	python:=os.Getenv("MEBOORU_METADATA_TEST_PYTHON")
	if !filepath.IsAbs(python){return managedqa.ErrFixture}
	if _,err:=processguard.ProbeSibling("monbooru","monloader");err!=nil{return err}
	self,err:=os.Executable();if err!=nil{return err}
	self,err=filepath.EvalSymlinks(self);if err!=nil{return err}
	temp,err:=filepath.EvalSymlinks(os.TempDir());if err!=nil{return err}
	dir,err:=os.MkdirTemp(temp,managedqa.Prefix);if err!=nil{return err}
	safe:=false
	defer func(){if safe{result=errors.Join(result,os.RemoveAll(dir))}else{fmt.Fprintln(os.Stderr,"retained disposable window:",dir)}}()
	for _,name:=range []string{"stage","gallery","queue","windows","data"}{if err:=os.Mkdir(filepath.Join(dir,name),0700);err!=nil{return err}}
	timeout := 10 * time.Minute
	if app && count == 200 {
		timeout = 20 * time.Minute
	}
	ctx,cancel:=context.WithTimeout(context.Background(),timeout);defer cancel()
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
	setup:=managedqa.Setup{Count:count,Windows:windows,Port:ln.Addr().(*net.TCPAddr).Port,Python:python,ScriptSHA256:managedqa.Digest([]byte(managedqa.PythonFixture)),Application:app}
	setup.CancelFirst = cancelFirst
	if app && count == 3 && !cancelFirst {
		setup.Destination = "Imports"
		if err := os.Mkdir(filepath.Join(dir,"second-gallery"),0700); err != nil { return err }
	}
	if err:=managedqa.Write(dir,managedqa.ScriptName,[]byte(managedqa.PythonFixture));err!=nil{return err}
	if err:=managedqa.WriteJSON(dir,managedqa.SetupName,setup);err!=nil{return err}
	if app {
		var err error
		if cancelFirst {
			err = probeActiveManagedCancel(ctx,self,dir,setup)
		} else {
			err = probeManagedApplication(ctx,self,dir,setup,imageBytes[2])
		}
		if err != nil { return err }
		requestMu.Lock()
		defer requestMu.Unlock()
		for id := 1; id <= count*windows; id++ {
			want := 0
			if !cancelFirst && id <= count && !setup.Blocked(1, id-1) { want = 1 }
			if cancelFirst && id > count && !setup.Blocked(2, id-count-1) { want = 1 }
			if requests[id] != want { return fmt.Errorf("post %d: %d media requests, expected %d", id, requests[id], want) }
		}
		safe = true
		return nil
	}
	cfg:=config.Gallery{Name:managedqa.Gallery,GalleryPath:filepath.Join(dir,"gallery"),DBPath:filepath.Join(dir,"gallery.sqlite"),ThumbnailsPath:filepath.Join(dir,"thumbs")}
	set,err:=library.OpenSet(windowHost{cfg},jobs.NewManager(),[]config.Gallery{cfg},cfg.Name);if err!=nil{return err}
	defer set.Close()
	registry,err:=windowregistry.Open(filepath.Join(dir,"windows"));if err!=nil{return err};defer registry.Close()
	o:=&managedWindowOwner{dir:dir,setup:setup,set:set,allowed:map[string]workerproto.StageAllocateRequest{},registry:registry,naming:config.GalleryConfig{DefaultUploadName:"{source}-{post_id}"}}
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
	totalCommitted:=0
	for job:=int64(1);job<=int64(windows);job++{
		md:=setup.MetadataFor(job)
		if _,err:=call(workerproto.QueuePause,nil);err!=nil{return err}
		intent,err:=registry.NewSubmission(ctx,workerproto.QueueCommand{URL:md.URL,Gallery:md.Gallery,MaxItems:count},o.galleryIdentity(md.Gallery));if err!=nil{return err}
		body,err:=workerproto.EncodeQueueCommand(workerproto.QueueEnqueue,intent.Command);if err!=nil{return err}
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
		o.mu.Unlock()
		if _,err:=call(workerproto.QueueResume,nil);err!=nil{return err}
		for{
			v,err=readJob();if err!=nil{return err}
			if v.Status=="succeeded"{break}
			if v.Status!="queued"&&v.Status!="running"{return fmt.Errorf("managed job %s: %s",v.Status,v.Error)}
			select{case <-ctx.Done():return ctx.Err();case <-time.After(200*time.Millisecond):}
		}
		o.mu.Lock();completedGrant:=o.window;o.mu.Unlock()
		settled,err:=registry.ObserveSettlement(completedGrant)
		if err!=nil||completedGrant.IntentID!=intent.Command.IntentID||completedGrant.Request!=md||len(v.Items)!=count{return managedqa.ErrFixture}
		if setup.AllBlocked(job){
			if settled.Kind!="no_transfer"||v.ManagedWindow!=nil{return managedqa.ErrFixture}
			for i,it:=range v.Items{if it.PostID!=strconv.Itoa(setup.Offset(job)+i+1)||it.Outcome!=string(contract.OutcomeSkippedBlacklist)||it.ImageID!=0||it.Receipt!=""{return managedqa.ErrFixture}}
			requestMu.Lock();exact:=len(requests)==totalCommitted;requestMu.Unlock()
			if !exact{return managedqa.ErrFixture}
			continue
		}
		if settled.Kind!="committed"||v.ManagedWindow==nil||v.ManagedWindow.State!="complete"{return managedqa.ErrFixture}
		totalCommitted+=count-1
		offset:=setup.Offset(job)
		if v.Items[0].PostID!=strconv.Itoa(offset+1)||v.Items[0].Outcome!=string(contract.OutcomeSkippedBlacklist){return managedqa.ErrFixture}
		for i,it:=range v.Items[1:]{
			if it.PostID!=strconv.Itoa(offset+i+2)||it.Outcome!=string(contract.OutcomeCreated)||it.ImageID<=0||len(it.Receipt)!=64{return managedqa.ErrFixture}
		}
		o.mu.Lock();observed:=o.result;o.mu.Unlock()
		if len(observed.Items)!=count-1||settled.PolicySHA256!=observed.PolicySHA256||settled.PlanSHA256!=observed.PlanSHA256{return managedqa.ErrFixture}
		g:=set.Get(cfg.Name)
		var n int
		if err:=g.DB.Read.QueryRowContext(ctx,"SELECT COUNT(*) FROM images WHERE is_missing=0").Scan(&n);err!=nil||n!=totalCommitted{return fmt.Errorf("library rows %d: %v",n,err)}
		for _,it:=range observed.Items{
			id,err:=strconv.Atoi(it.PostID);if err!=nil||it.State!="committed"||it.SHA256!=managedqa.Digest(imageBytes[id]){return managedqa.ErrFixture}
			var path string
			if err:=g.DB.Read.QueryRowContext(ctx,"SELECT canonical_path FROM images WHERE id=?",it.ImageID).Scan(&path);err!=nil{return err}
			got,err:=os.ReadFile(path);if err!=nil||!bytes.Equal(got,imageBytes[id]){return managedqa.ErrFixture}
		}
		requestMu.Lock()
		exact:=len(requests)==totalCommitted&&requests[offset+1]==0
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
	if windows==1{fmt.Printf("PASS: disposable managed executable window %d listed, %d committed, one blocked, exact receipts and cleanup\n",count,count-1)}else if windows==2{fmt.Println("PASS: two successive managed executable windows on one worker session, exact durable settlement")}else{fmt.Println("PASS: three successive windows, middle fully blocked with independent no-transfer settlement, four committed")}
	return nil
}
