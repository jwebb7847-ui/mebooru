package acquisitionstage

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/library"
)

func TestExistingPublicationTarget(t *testing.T) {
	testExistingPublicationTargetModes(t, []string{"held", "held nested", "held alias", "restore marked", "restore unmarked", "missing target", "wrong disposition", "canonical changed", "canonical hardlink", "restore alias", "restore file returned", "wrong DB facts", "nonempty job", "no authority", "revoked", "changed during inspection", "changed note", "changed canonical identity", "restore row changed", "attempt exists"})
}

func TestDurableExistingTargetPlan(t *testing.T){
	testExistingPublicationTargetModes(t, []string{"plan held", "restore plan", "plan reopen", "plan forged", "plan stale", "plan wrong decision", "plan no decision", "plan revoke after marker", "plan changed after marker", "plan corrupt", "plan noncanonical", "plan changed record", "plan lost marker", "plan lost anchor", "plan partial anchor"})
}

func TestGuardedExistingExecution(t *testing.T){
	testExistingPublicationTargetModes(t, []string{"exec held","exec alias","restore exec","restore exec unmarked","exec reopen","exec stale","exec revoked after link intent","exec changed after link","exec revoked after landing intent","exec changed after landing intent","restore exec naming changed","restore exec bad naming","restore exec old path returned","exec lost link","exec lost landing","exec changed after landing"})
}

func TestGuardedExistingMetadata(t *testing.T){
	testExistingPublicationTargetModes(t, []string{"exec meta held","restore exec meta","exec meta alias","exec meta reopen","exec meta revoked","exec meta revoked after intent","exec meta changed after intent","exec meta source failure","exec meta source conflict","exec meta changed after result","exec meta changed source","exec meta lost result","exec meta lost intent","exec meta corrupt result","exec meta changed path","exec meta revoked after result","exec meta unexpected claim","exec meta changed membership"})
}

func testExistingPublicationTargetModes(t *testing.T, modes []string){
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			var buf bytes.Buffer
			if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
				t.Fatal(err)
			}
			data := buf.Bytes()
			a, stage, s, grant, authority, f := publicationFixtureData(t, "png", data)
			defer func(){ if err := a.Close(); err != nil { t.Error(err) } }()
			row := acquisitionplan.ApprovedPolicyRow{PostID:f.ID, NativeSHA256:strings.Repeat("a",64), MediaURL:"https://example.invalid/42.png", SourceLabel:"derpibooru", PostURL:"https://derpibooru.org/images/42", Rating:"general", Tags:[]string{"fixture","rating:general"}, Provenance:&acquisitionplan.ApprovedProvenance{}}
			if strings.Contains(mode,"exec meta"){row.Provenance.Commentary="fixture commentary";row.Provenance.Original="https://example.invalid/original"}
			body, hash, err := acquisitionplan.EncodeApprovedPolicy(grant.Request.Transfer, []acquisitionplan.ApprovedPolicyRow{row})
			if err != nil { t.Fatal(err) }
			if err := a.BindPolicy(ctx, grant, func(context.Context, workerproto.StageAllocation)([]byte,string,error){return body,hash,nil}); err != nil { t.Fatal(err) }
			g, err := library.Open(config.Gallery{Name:"A",GalleryPath:s.GalleryRoot,DBPath:filepath.Join(filepath.Dir(stage),"gallery.sqlite"),ThumbnailsPath:filepath.Join(filepath.Dir(stage),"thumbs")})
			if err != nil { t.Fatal(err) }
			defer g.Close()
			old := filepath.Join(s.GalleryRoot,"existing.png")
			if mode == "held nested" {
				old = filepath.Join(s.GalleryRoot,"nested","existing.png")
				if err := os.Mkdir(filepath.Dir(old),0700); err != nil { t.Fatal(err) }
			}
			var id int64
			if mode != "missing target" {
				if err := os.WriteFile(old,data,0600); err != nil { t.Fatal(err) }
				res, err := g.LandInPlace(ctx,old,library.LandSpec{Origin:"fixture"})
				if err != nil || res.Image == nil || res.Duplicate { t.Fatal("existing fixture",err) }
				id = res.Image.ID
				if _,_,err := gallery.MergeSource(g.DB,g.TagSvc,id,"other","7","https://example.invalid/7","","",gallery.PostFile{},[]string{"rating:explicit","prior"},gallery.Withheld{}); err != nil { t.Fatal(err) }
				if _,err := g.DB.Write.Exec("UPDATE images SET note='operator note' WHERE id=?",id); err != nil { t.Fatal(err) }
				if strings.Contains(mode,"exec meta"){
					order:=3
					if err:=gallery.SetHomeCollection(g.DB,id,"original-home",&order);err!=nil{t.Fatal(err)}
					if _,err:=g.DB.Write.Exec("INSERT INTO image_annotations(image_id,x,y,w,h,body,manual) VALUES(?,1,2,3,4,'operator annotation',1)",id);err!=nil{t.Fatal(err)}
				}
			}
			kind := existingHeld
			if strings.HasPrefix(mode,"restore") { kind = existingRestore }
			if mode == "held alias" || mode == "restore alias" || mode == "exec alias" || mode == "exec meta alias" {
				alias := filepath.Join(s.GalleryRoot,"operator.png")
				if err := os.WriteFile(alias,data,0600); err != nil { t.Fatal(err) }
				if res,err := g.LandInPlace(ctx,alias,library.LandSpec{Origin:"fixture"}); err != nil || !res.Duplicate { t.Fatal("alias",err) }
			}
			if kind == existingRestore || mode == "wrong disposition" {
				if err := os.Remove(old); err != nil { t.Fatal(err) }
				if mode != "restore unmarked" && mode != "restore exec unmarked" {
					if _,err := gallery.SetMissing(g.DB,id,true); err != nil { t.Fatal(err) }
				}
			}
			switch mode {
			case "canonical changed":
				if err := os.WriteFile(old, bytes.Repeat([]byte{'x'},len(data)),0600); err != nil { t.Fatal(err) }
			case "canonical hardlink":
				if err := os.Link(old,filepath.Join(s.GalleryRoot,"hardlink.png")); err != nil { t.Fatal(err) }
			case "restore file returned":
				if err := os.WriteFile(old,data,0600); err != nil { t.Fatal(err) }
			case "wrong DB facts":
				if _,err := g.DB.Write.Exec("UPDATE images SET width=17 WHERE id=?",id); err != nil { t.Fatal(err) }
			}
			if err := a.preparePublication(ctx,grant); err != nil { t.Fatal(err) }
			intent := filepath.Join(stage,grant.Request.OperationID,"publication","attempt")
			if mode == "attempt exists" {
				if _,err := a.publishPrepared(ctx,grant); err != nil { t.Fatal(err) }
			}
			job := contract.JobExtras{}
			if mode == "nonempty job" { job.Tags = []string{"not supported"} }
			calls := 0
			extras := extrasAuthority(func(context.Context,workerproto.StageAllocation)(contract.JobExtras,error){
				calls++
				if mode == "changed during inspection" && calls == 2 {
					if _,err := g.DB.Write.Exec("UPDATE images SET note='changed' WHERE id=?",id); err != nil { t.Fatal(err) }
				}
				return job,nil
			})
			if mode == "no authority" { extras = nil }
			if mode == "revoked" { a.authority = func(context.Context,workerproto.StageAllocateRequest)(Scope,error){ return Scope{},ErrStageRefused } }
			count := func() [2]int {
				t.Helper()
				var n [2]int
				if err := g.DB.Read.QueryRow("SELECT COUNT(*) FROM images").Scan(&n[0]); err != nil { t.Fatal(err) }
				if err := g.DB.Read.QueryRow("SELECT COUNT(*) FROM image_paths").Scan(&n[1]); err != nil { t.Fatal(err) }
				return n
			}
			before := count()
			got,err := a.inspectExistingPublicationTarget(ctx,grant,g,kind,extras)
			want := mode == "held" || mode == "held nested" || mode == "held alias" || mode == "restore marked" || mode == "restore unmarked" || mode == "changed note" || mode == "changed canonical identity" || mode == "restore row changed"
			planMode := strings.HasPrefix(mode,"plan ")||mode=="restore plan"
			execMode:=strings.HasPrefix(mode,"exec ")||strings.HasPrefix(mode,"restore exec")
			want = want||planMode||execMode
			if (err == nil) != want { t.Fatal("unexpected eligibility",got,err) }
			if count() != before { t.Fatal("observer changed rows") }
			if mode != "attempt exists" {
				if _,err := os.Stat(intent); !os.IsNotExist(err) { t.Fatal("observer wrote publication attempt",err) }
				if _,err := os.Stat(filepath.Join(s.GalleryRoot,finalName(grant,f))); !os.IsNotExist(err) { t.Fatal("observer placed file",err) }
			}
			if _,err := os.Stat(filepath.Join(filepath.Dir(intent),"landing-attempt.json")); !os.IsNotExist(err) { t.Fatal("observer wrote landing attempt",err) }
			if !want { return }
			if got.ImageID != id || got.Kind != kind || got.PolicySHA256 != hash || got.FileIdentity == "" || !validDigest(got.StateSHA256) { t.Fatal("wrong evidence",got) }
			if kind == existingHeld {
				if got.Held == nil || got.Restore != nil || got.Held.ImageID != id || got.Held.CanonicalIdentity == "" { t.Fatal("held evidence",got) }
				if mode == "held alias" && len(got.Retained) != 2 { t.Fatal("alias evidence missing",got) }
			} else {
				if got.Restore == nil || got.Held != nil || got.Restore.RestoreImageID != id || got.Restore.RestorePathID <= 0 || got.Restore.RestoreMarkedMissing != (mode != "restore unmarked" && mode != "restore exec unmarked") || got.Restore.RestoreRating != "explicit" { t.Fatal("restore evidence",got) }
			}
			again,err := a.inspectExistingPublicationTarget(ctx,grant,g,kind,extras)
			if err != nil || !sameExistingPublicationTarget(got,again) { t.Fatal("stable repeat",again,err) }
			if execMode {
				checkGuardedExistingExecution(t,ctx,&a,stage,grant,g,got,extras,authority,mode,count,before,old,data)
				return
			}
			if planMode {
				checkDurableExistingTargetPlan(t,ctx,&a,stage,grant,g,got,extras,authority,mode,count,before)
				return
			}
			switch mode {
			case "changed note":
				if _,err := g.DB.Write.Exec("UPDATE images SET note='new note' WHERE id=?",id); err != nil { t.Fatal(err) }
			case "changed canonical identity":
				if err := os.Rename(old,filepath.Join(filepath.Dir(stage),"retained-original.png")); err != nil { t.Fatal(err) }
				if err := os.WriteFile(old,data,0600); err != nil { t.Fatal(err) }
			case "restore row changed":
				if _,err := g.DB.Write.Exec("UPDATE image_paths SET id=id+10000 WHERE path=?",old); err != nil { t.Fatal(err) }
			default:
				return
			}
			changed,err := a.inspectExistingPublicationTarget(ctx,grant,g,kind,extras)
			if err != nil || sameExistingPublicationTarget(got,changed) { t.Fatal("changed target not distinguished",changed,err) }
			if count() != before { t.Fatal("observer changed rows") }
		})
	}
}

// No publication, landing, deletion or DB mutation is authorized by a
// persisted existing-target decision. Changed or corrupt records refuse.
func checkDurableExistingTargetPlan(t *testing.T,ctx context.Context,ap **Allocator,stage string,grant workerproto.StageAllocation,g *library.Gallery,got existingPublicationTarget,extras extrasAuthority,authority Authority,mode string,count func()[2]int,before [2]int){
	t.Helper()
	a:=*ap
	dir:=filepath.Join(stage,grant.Request.OperationID,"publication")
	path:=filepath.Join(dir,existingTargetPlanName)
	anchor:=filepath.Join(dir,existingTargetAnchorName)
	if mode=="plan partial anchor"{
		if err:=os.WriteFile(anchor,[]byte(strings.Repeat("a",64)),0600);err!=nil{t.Fatal(err)}
	}
	injected:=false
	decision:=existingDispositionAuthority(func(context.Context,workerproto.StageAllocation)(string,error){
		if mode=="plan wrong decision"{return existingRestore,nil}
		if _,err:=os.Stat(path);err==nil&&!injected{
			injected=true
			if mode=="plan revoke after marker"{return "",ErrStageRefused}
			if mode=="plan changed after marker"{
				if _,err:=g.DB.Write.Exec("UPDATE images SET note='changed' WHERE id=?",got.ImageID);err!=nil{t.Fatal(err)}
			}
		}
		return got.Kind,nil
	})
	if mode=="plan no decision"{decision=nil}
	expected:=got
	if mode=="plan forged"{expected.ImageID++}
	if mode=="plan stale"{if _,err:=g.DB.Write.Exec("UPDATE images SET note='changed' WHERE id=?",got.ImageID);err!=nil{t.Fatal(err)}}
	err:=a.recordExistingTargetPlan(ctx,grant,g,expected,decision,extras)
	pre:=mode=="plan forged"||mode=="plan stale"||mode=="plan wrong decision"||mode=="plan no decision"||mode=="plan partial anchor"
	late:=mode=="plan revoke after marker"||mode=="plan changed after marker"
	if (err!=nil)!=(pre||late){t.Fatal("record result",err)}
	if count()!=before{t.Fatal("record changed gallery rows")}
	if pre{
		if _,err:=os.Stat(path);!os.IsNotExist(err){t.Fatal("premature plan",err)}
		if mode=="plan partial anchor"{
			if _,err:=a.publishPrepared(ctx,grant);err==nil{t.Fatal("partial anchor allowed publication")}
			if _,err:=os.Stat(anchor);err!=nil{t.Fatal("partial anchor removed",err)}
		}else if _,err:=os.Stat(anchor);!os.IsNotExist(err){t.Fatal("premature anchor",err)}
		return
	}
	raw,err:=os.ReadFile(path)
	if err!=nil||len(raw)==0{t.Fatal("missing durable plan",err)}
	if b,err:=os.ReadFile(anchor);err!=nil||string(b)!=digestBytes(raw){t.Fatal("wrong plan anchor",err)}
	// All older publication and landing entry points must remain blocked.
	if _,err:=a.publishPrepared(ctx,grant);err==nil{t.Fatal("diagnostic publication consumed plan")}
	if err:=a.beginLanding(ctx,grant,g);err==nil{t.Fatal("diagnostic landing consumed plan")}
	if err:=a.landRestoredPrepared(ctx,grant,g);err==nil{t.Fatal("diagnostic restoration consumed plan")}
	if err:=a.landNewPrepared(ctx,grant,g);err==nil{t.Fatal("new diagnostic consumed plan")}
	naming:=namingAuthority(func(context.Context,workerproto.StageAllocation)(gallery.ReceivedNamingRequest,error){return gallery.ReceivedNamingRequest{Gallery:g.Name,ReceivedName:got.File.RelativeName,Source:"derpibooru",PostID:got.File.ID},nil})
	if _,err:=a.planNewPublication(ctx,grant,g,naming,extras);err==nil{t.Fatal("new planning consumed plan")}
	if err:=a.recordExistingTargetPlan(ctx,grant,g,got,decision,extras);err==nil{t.Fatal("plan overwritten")}
	if b,err:=os.ReadFile(path);err!=nil||!bytes.Equal(b,raw){t.Fatal("plan changed",err)}
	if _,err:=os.Stat(filepath.Join(dir,"attempt"));!os.IsNotExist(err){t.Fatal("unexpected publication marker",err)}
	if _,err:=os.Stat(filepath.Join(dir,"landing-attempt.json"));!os.IsNotExist(err){t.Fatal("unexpected landing marker",err)}
	if count()!=before{t.Fatal("blocked paths changed rows")}
	if late{
		if mode=="plan changed after marker"{
			if _,err:=a.observeExistingTargetPlan(ctx,grant,g,decision,extras);err==nil{t.Fatal("changed target acknowledged")}
		}else{
			// Lost acknowledgement may be resolved only with renewed decision.
			if m,err:=a.observeExistingTargetPlan(ctx,grant,g,decision,extras);err!=nil||!sameExistingPublicationTarget(m.Target,got){t.Fatal("renewed observation",err)}
		}
		return
	}
	switch mode{
	case "plan reopen":
		if err:=a.Close();err!=nil{t.Fatal(err)}
		next,err:=Open(stage,2,4096,authority)
		if err!=nil{t.Fatal(err)}
		*ap=next;a=next
	case "plan corrupt":
		if err:=os.WriteFile(path,[]byte("corrupt"),0600);err!=nil{t.Fatal(err)}
	case "plan noncanonical":
		if err:=os.WriteFile(path,append(raw,'\n'),0600);err!=nil{t.Fatal(err)}
	case "plan changed record":
		// Same canonical JSON shape, wrong target identity.
		changed:=bytes.Replace(raw,[]byte(got.StateSHA256),[]byte(strings.Repeat("b",64)),1)
		if bytes.Equal(raw,changed){t.Fatal("fixture unchanged")}
		if err:=os.WriteFile(path,changed,0600);err!=nil{t.Fatal(err)}
	case "plan lost marker":
		if err:=os.Remove(path);err!=nil{t.Fatal(err)}
	case "plan lost anchor":
		if err:=os.Remove(anchor);err!=nil{t.Fatal(err)}
	}
	m,err:=a.observeExistingTargetPlan(ctx,grant,g,decision,extras)
	want:=mode=="plan held"||mode=="restore plan"||mode=="plan reopen"
	if (err==nil)!=want||want&&!sameExistingPublicationTarget(m.Target,got){t.Fatal("observation",m,err)}
	if _,err:=a.publishPrepared(ctx,grant);err==nil{t.Fatal("damaged or valid plan allowed publication")}
	if count()!=before{t.Fatal("observer changed gallery rows")}
}

func checkGuardedExistingExecution(t *testing.T,ctx context.Context,ap **Allocator,stage string,grant workerproto.StageAllocation,g *library.Gallery,got existingPublicationTarget,extras extrasAuthority,authority Authority,mode string,count func()[2]int,before [2]int,old string,data []byte){
	t.Helper()
	a:=*ap
	dir:=filepath.Join(stage,grant.Request.OperationID,"publication")
	link:=filepath.Join(dir,existingLinkAttemptName)
	landing:=filepath.Join(dir,existingLandingAttemptName)
	managed:=filepath.Join(g.GalleryPath,finalName(grant,got.File))
	decision:=existingDispositionAuthority(func(context.Context,workerproto.StageAllocation)(string,error){
		if mode=="exec revoked after link intent"{if _,err:=os.Stat(link);err==nil{return "",ErrStageRefused}}
		if mode=="exec revoked after landing intent"{if _,err:=os.Stat(landing);err==nil{return "",ErrStageRefused}}
		return got.Kind,nil
	})
	req:=gallery.ReceivedNamingRequest{Gallery:g.Name,ReceivedName:got.File.RelativeName,Source:"derpibooru",PostID:got.File.ID,DefaultName:"{source}-{post_id}"}
	if mode=="restore exec bad naming"{req.DefaultName="{date}"}
	naming:=namingAuthority(func(context.Context,workerproto.StageAllocation)(gallery.ReceivedNamingRequest,error){return req,nil})
	injected:=false
	originalExtras:=extras
	extras=func(ctx context.Context,grant workerproto.StageAllocation)(contract.JobExtras,error){
		if mode=="exec changed after landing intent"&&!injected{
			if _,err:=os.Stat(landing);err==nil{
				injected=true
				if _,err:=g.DB.Write.Exec("UPDATE images SET note='late change' WHERE id=?",got.ImageID);err!=nil{t.Fatal(err)}
			}
		}
		return originalExtras(ctx,grant)
	}
	if err:=a.recordExistingTargetPlan(ctx,grant,g,got,decision,extras);err!=nil{t.Fatal("record",err)}
	if mode=="exec stale"{if _,err:=g.DB.Write.Exec("UPDATE images SET note='stale' WHERE id=?",got.ImageID);err!=nil{t.Fatal(err)}}
	err:=a.publishExistingTarget(ctx,grant,g,decision,extras,naming)
	noLink:=mode=="exec stale"||mode=="restore exec bad naming"||mode=="exec revoked after link intent"
	if (err!=nil)!=noLink{t.Fatal("publication",err)}
	if count()!=before{t.Fatal("publication changed DB")}
	if noLink{
		if _,err:=os.Stat(managed);!os.IsNotExist(err){t.Fatal("unexpected placement",err)}
		if mode=="exec revoked after link intent"{
			if _,err:=os.Stat(link);err!=nil{t.Fatal("missing link intent",err)}
			if err:=a.publishExistingTarget(ctx,grant,g,decision,extras,naming);err==nil{t.Fatal("replayed failed link")}
		}else if _,err:=os.Stat(link);!os.IsNotExist(err){t.Fatal("premature link intent",err)}
		return
	}
	if b,err:=os.ReadFile(managed);err!=nil||!bytes.Equal(b,data){t.Fatal("placement bytes",err)}
	if err:=a.publishExistingTarget(ctx,grant,g,decision,extras,naming);err==nil{t.Fatal("link replay")}
	if err:=a.beginLanding(ctx,grant,g);err==nil{t.Fatal("legacy landing bypass")}
	if err:=a.landRestoredPrepared(ctx,grant,g);err==nil{t.Fatal("legacy restoration bypass")}
	if _,err:=a.applyMappedMetadata(ctx,grant,g);err==nil{t.Fatal("premature metadata")}
	if _,err:=a.observeExistingLanding(ctx,grant,g,decision,extras,naming);err==nil{t.Fatal("premature landing witness")}
	switch mode{
	case "exec changed after link":
		if _,err:=g.DB.Write.Exec("UPDATE images SET note='late' WHERE id=?",got.ImageID);err!=nil{t.Fatal(err)}
	case "restore exec naming changed":
		req.DefaultName="changed"
	case "restore exec old path returned":
		if err:=os.WriteFile(old,[]byte("operator"),0600);err!=nil{t.Fatal(err)}
	case "exec lost link":
		if err:=os.Remove(link);err!=nil{t.Fatal(err)}
	}
	err=a.landExistingTarget(ctx,grant,g,decision,extras,naming)
	pre:=mode=="exec changed after link"||mode=="restore exec naming changed"||mode=="restore exec old path returned"||mode=="exec lost link"
	late:=mode=="exec revoked after landing intent"||mode=="exec changed after landing intent"
	if (err!=nil)!=(pre||late){t.Fatal("landing",err)}
	if pre||late{
		if count()!=before{t.Fatal("refused landing changed rows")}
		if pre{if _,err:=os.Stat(landing);!os.IsNotExist(err){t.Fatal("premature landing intent",err)}}else{
			if _,err:=os.Stat(landing);err!=nil{t.Fatal("missing landing intent",err)}
			if err:=a.landExistingTarget(ctx,grant,g,decision,extras,naming);err==nil{t.Fatal("replayed landing")}
		}
		return
	}
	if count()[0]!=before[0]{t.Fatal("existing image ID replaced")}
	if err:=a.landExistingTarget(ctx,grant,g,decision,extras,naming);err==nil{t.Fatal("landing replay")}
	if _,err:=a.applyMappedMetadata(ctx,grant,g);err==nil{t.Fatal("legacy metadata bypass")}
	if _,err:=a.observeTerminalReceipt(ctx,grant,g,naming,extras);err==nil{t.Fatal("premature receipt")}
	if got.Kind==existingHeld{
		if b,err:=os.ReadFile(old);err!=nil||!bytes.Equal(b,data){t.Fatal("canonical bytes",err)}
		if mode=="exec alias"||mode=="exec meta alias"{if b,err:=os.ReadFile(filepath.Join(g.GalleryPath,"operator.png"));err!=nil||!bytes.Equal(b,data){t.Fatal("operator alias",err)}}
	}else if _,err:=os.Stat(old);!os.IsNotExist(err){t.Fatal("old path reappeared",err)}
	if strings.Contains(mode,"exec meta"){
		checkGuardedExistingMetadata(t,ctx,ap,stage,grant,g,got,extras,authority,mode,naming,decision,old,data)
		return
	}
	switch mode{
	case "exec reopen":
		if err:=a.Close();err!=nil{t.Fatal(err)}
		next,err:=Open(stage,2,4096,authority);if err!=nil{t.Fatal(err)}
		*ap=next;a=next
	case "exec lost landing":
		if err:=os.Remove(landing);err!=nil{t.Fatal(err)}
	case "exec changed after landing":
		if _,err:=g.DB.Write.Exec("UPDATE images SET note='changed' WHERE id=?",got.ImageID);err!=nil{t.Fatal(err)}
	}
	row,err:=a.observeExistingLanding(ctx,grant,g,decision,extras,naming)
	want:=mode!="exec lost landing"&&mode!="exec changed after landing"
	if (err==nil)!=want||want&&(row.ImageID!=got.ImageID||row.Canonical!=(got.Kind==existingRestore)){t.Fatal("witness",row,err)}
	if _,err:=os.Stat(filepath.Join(dir,"attempt"));!os.IsNotExist(err){t.Fatal("legacy attempt",err)}
	if _,err:=os.Stat(filepath.Join(dir,"landing-attempt.json"));!os.IsNotExist(err){t.Fatal("legacy landing marker",err)}
}

func checkGuardedExistingMetadata(t *testing.T,ctx context.Context,ap **Allocator,stage string,grant workerproto.StageAllocation,g *library.Gallery,got existingPublicationTarget,extras extrasAuthority,authority Authority,mode string,naming namingAuthority,decision existingDispositionAuthority,old string,data []byte){
	t.Helper()
	a:=*ap
	dir:=filepath.Join(stage,grant.Request.OperationID,"publication")
	attempt:=filepath.Join(dir,existingMetadataAttemptName)
	result:=filepath.Join(dir,existingMetadataResultName)
	managed:=filepath.Join(g.GalleryPath,finalName(grant,got.File))
	id:=got.ImageID
	if _,err:=a.observeExistingMetadata(ctx,grant,g,decision,extras,naming);err==nil{t.Fatal("premature metadata observation")}
	if mode=="exec meta source failure"{
		if _,err:=g.DB.Write.Exec("CREATE TRIGGER reject_fixture_source BEFORE INSERT ON image_sources WHEN NEW.site='derpibooru' BEGIN SELECT RAISE(ABORT,'fixture'); END");err!=nil{t.Fatal(err)}
	}
	if mode=="exec meta source conflict"{
		if _,_,err:=gallery.MergeSource(g.DB,g.TagSvc,id,"derpibooru","other-post","https://example.invalid/other-post","","",gallery.PostFile{},[]string{"prior"},gallery.Withheld{});err!=nil{t.Fatal(err)}
	}
	original:=decision
	injected:=false
	decision=func(ctx context.Context,grant workerproto.StageAllocation)(string,error){
		if mode=="exec meta revoked"{return "",ErrStageRefused}
		if _,err:=os.Stat(attempt);err==nil&&!injected{
			if mode=="exec meta revoked after intent"{return "",ErrStageRefused}
			if mode=="exec meta changed after intent"{
				injected=true
				if _,err:=g.DB.Write.Exec("UPDATE images SET note='late change' WHERE id=?",id);err!=nil{t.Fatal(err)}
			}
		}
		if mode=="exec meta revoked after result"{if _,err:=os.Stat(result);err==nil{return "",ErrStageRefused}}
		return original(ctx,grant)
	}
	v,err:=a.applyExistingMetadata(ctx,grant,g,decision,extras,naming)
	pre:=mode=="exec meta revoked"||mode=="exec meta source conflict"
	partial:=mode=="exec meta revoked after intent"||mode=="exec meta changed after intent"||mode=="exec meta source failure"
	late:=mode=="exec meta revoked after result"
	if (err!=nil)!=(pre||partial||late){t.Fatal("metadata",v,err)}
	if pre{if _,err:=os.Stat(attempt);!os.IsNotExist(err){t.Fatal("premature metadata intent",err)};return}
	if _,err:=os.Stat(attempt);err!=nil{t.Fatal("missing metadata intent",err)}
	if partial{
		if _,err:=os.Stat(result);!os.IsNotExist(err){t.Fatal("partial write got result",err)}
		if _,err:=a.applyExistingMetadata(ctx,grant,g,decision,extras,naming);err==nil{t.Fatal("metadata replay")}
		if _,err:=a.observeExistingMetadata(ctx,grant,g,decision,extras,naming);err==nil{t.Fatal("partial metadata acknowledged")}
		return
	}
	if late{decision=original;v,err=a.observeExistingMetadata(ctx,grant,g,decision,extras,naming);if err!=nil{t.Fatal("lost acknowledgement",err)}}
	if v.ImageID!=id||v.PostID!=got.File.ID||v.Rating!="explicit"{t.Fatal("wrong metadata evidence",v)}
	var n int
	if err:=g.DB.Read.QueryRow("SELECT COUNT(*) FROM image_sources WHERE image_id=? AND site IN ('other','derpibooru')",id).Scan(&n);err!=nil||n!=2{t.Fatal("source memberships",n,err)}
	if err:=g.DB.Read.QueryRow("SELECT COUNT(*) FROM images WHERE id=? AND note='operator note'",id).Scan(&n);err!=nil||n!=1{t.Fatal("operator note",n,err)}
	if err:=g.DB.Read.QueryRow("SELECT COUNT(*) FROM image_collections WHERE image_id=? AND name='original-home' AND position=3",id).Scan(&n);err!=nil||n!=1{t.Fatal("prior collection",n,err)}
	if err:=g.DB.Read.QueryRow("SELECT COUNT(*) FROM image_annotations WHERE image_id=? AND manual=1 AND body='operator annotation'",id).Scan(&n);err!=nil||n!=1{t.Fatal("prior manual annotation",n,err)}
	if err:=g.DB.Read.QueryRow("SELECT COUNT(*) FROM image_sources WHERE image_id=? AND site='derpibooru' AND commentary='fixture commentary' AND original='https://example.invalid/original'",id).Scan(&n);err!=nil||n!=1{t.Fatal("source provenance",n,err)}
	if _,err:=a.applyExistingMetadata(ctx,grant,g,decision,extras,naming);err==nil{t.Fatal("metadata replay")}
	if _,err:=a.applyMappedMetadata(ctx,grant,g);err==nil{t.Fatal("legacy metadata bypass")}
	if _,err:=a.observeTerminalReceipt(ctx,grant,g,naming,extras);err==nil{t.Fatal("premature terminal receipt")}
	if b,err:=os.ReadFile(managed);err!=nil||!bytes.Equal(b,data){t.Fatal("managed bytes",err)}
	if got.Kind==existingHeld{if b,err:=os.ReadFile(old);err!=nil||!bytes.Equal(b,data){t.Fatal("canonical bytes",err)}}
	switch mode{
	case "exec meta reopen":
		if err:=a.Close();err!=nil{t.Fatal(err)}
		next,err:=Open(stage,2,4096,authority);if err!=nil{t.Fatal(err)}
		*ap=next;a=next
	case "exec meta changed after result":
		if _,err:=g.DB.Write.Exec("UPDATE images SET note='new note' WHERE id=?",id);err!=nil{t.Fatal(err)}
	case "exec meta changed source":
		if _,err:=g.DB.Write.Exec("UPDATE image_sources SET url='https://example.invalid/changed' WHERE image_id=? AND site='other'",id);err!=nil{t.Fatal(err)}
	case "exec meta lost result":
		if err:=os.Remove(result);err!=nil{t.Fatal(err)}
	case "exec meta lost intent":
		if err:=os.Remove(attempt);err!=nil{t.Fatal(err)}
	case "exec meta corrupt result":
		if err:=os.WriteFile(result,[]byte("bad"),0600);err!=nil{t.Fatal(err)}
	case "exec meta unexpected claim":
		if _,err:=g.DB.Write.Exec("UPDATE image_sources SET parent_url='https://example.invalid/unapproved' WHERE image_id=? AND site='derpibooru'",id);err!=nil{t.Fatal(err)}
	case "exec meta changed membership":
		if _,err:=g.DB.Write.Exec("UPDATE image_collections SET position=99 WHERE image_id=?",id);err!=nil{t.Fatal(err)}
	case "exec meta changed path":
		if _,err:=g.DB.Write.Exec("UPDATE image_paths SET id=id+10000 WHERE path=?",managed);err!=nil{t.Fatal(err)}
	}
	again,err:=a.observeExistingMetadata(ctx,grant,g,decision,extras,naming)
	want:=mode=="exec meta held"||mode=="restore exec meta"||mode=="exec meta alias"||mode=="exec meta reopen"||mode=="exec meta revoked after result"
	if (err==nil)!=want||want&&!sameMetadataObservation(again,v){t.Fatal("metadata observation",again,err)}
}
