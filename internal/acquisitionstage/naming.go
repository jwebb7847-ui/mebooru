package acquisitionstage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/db"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/library"
)

// Isolated single-new or explicitly restored PNG, existing destination
// directory. New-only may include verified collection and client tags;
// restored naming is restricted to an explicitly empty owner job.
// The trusted caller supplies an independent current naming decision,
// never a worker-supplied destination. No production authority yet.
// Retain both private copies and refuse replay after any attempt.
type namingAuthority func(context.Context,workerproto.StageAllocation)(gallery.ReceivedNamingRequest,error)
const maxNamingRecord = 128 << 10

type namingAttempt struct {
	Version int `json:"version"`
	MetadataSHA256 string `json:"metadata_sha256"`
	RelationshipSHA256 string `json:"relationship_sha256"`
	CollectionSHA256 string `json:"collection_sha256,omitempty"`
	RestoreStateSHA256 string `json:"restore_state_sha256,omitempty"`
	Request gallery.ReceivedNamingRequest `json:"request"`
	ImageID int64 `json:"image_id"`
	PathID int64 `json:"path_id"`
	Old string `json:"old"`
	Initial string `json:"initial"`
	Final string `json:"final"`
	FileIdentity string `json:"file_identity"`
	InitialSuffixed bool `json:"initial_suffixed"`
	FinalSuffixed bool `json:"final_suffixed"`
}
type namingResult struct {
	Version int `json:"version"`
	AttemptSHA256 string `json:"attempt_sha256"`
}
func validNamingRelative(s string)bool{
	return s!=""&&s!="."&&len(s)<=4096&&filepath.IsLocal(s)&&filepath.Clean(s)==s&&!strings.ContainsAny(s,"\x00:")
}
func validNamingRequest(q gallery.ReceivedNamingRequest)bool{
	for _,s:=range []string{q.Gallery,q.ReceivedName,q.RequestFolder,q.DefaultFolder,q.DefaultName,q.Source,q.PostID}{
		if len(s)>4096||strings.ContainsRune(s,0){return false}
	}
	return q.Gallery!=""&&q.ReceivedName!=""&&q.Source!=""&&q.PostID!=""
}
func readNamingAttempt(p *os.Root,r record,e SealedEvidence,result metadataResult)(namingAttempt,[]byte,error){
	var zero namingAttempt
	raw,err:=boundedRegularBytes(p,"naming-attempt.json",maxNamingRecord)
	if err!=nil{return zero,nil,err}
	var m namingAttempt
	meta,err:=json.Marshal(result)
	if err!=nil||len(e.Files)!=1||len(result.Rows)!=1||json.Unmarshal(raw,&m)!=nil||m.Version!=1||
		m.MetadataSHA256!=digestBytes(meta)||m.ImageID!=result.Rows[0].ImageID||m.PathID<=0||
		m.Old!=finalName(r.Allocation,e.Files[0])||!validNamingRelative(m.Initial)||!validNamingRelative(m.Final)||
		strings.EqualFold(m.Old,m.Final)||m.FileIdentity==""||len(m.FileIdentity)>128||
		!validNamingRequest(m.Request)||m.Request.Gallery!=r.Allocation.Request.Transfer.Metadata.Gallery||
		m.Request.ReceivedName!=e.Files[0].RelativeName||m.Request.PostID!=e.Files[0].ID{return zero,nil,ErrStageRecovery}
	rel,err:=boundedRegularBytes(p,"relationship-result.json",maxRelationshipRecord)
	if err!=nil||m.RelationshipSHA256!=digestBytes(rel){return zero,nil,ErrStageRecovery}
	collection,err:=collectionResultDigest(p)
	if err!=nil||m.CollectionSHA256!=collection{return zero,nil,ErrStageRecovery}
	canonical,err:=json.Marshal(m)
	if err!=nil||!bytes.Equal(raw,canonical){return zero,nil,ErrStageRecovery}
	return m,raw,nil
}
func validateNamingRecords(p *os.Root,r record,e SealedEvidence,l landingAttempt,result metadataResult,hasResult bool)error{
	if !l.NewOnly&&l.RestoreImageID<=0{return ErrStageRecovery}
	m,raw,err:=readNamingAttempt(p,r,e,result)
	if err!=nil{return err}
	if l.NewOnly{
		if l.RestoreImageID!=0||m.RestoreStateSHA256!=""{return ErrStageRecovery}
	}else if l.RestorePathID<=0||!validDigest(l.RestorePreservationSHA256)||!validDigest(m.RestoreStateSHA256)||m.ImageID!=l.RestoreImageID||m.CollectionSHA256!=""||strings.EqualFold(m.Final,l.RestorePath)||strings.EqualFold(m.Initial,l.RestorePath){return ErrStageRecovery}
	if !hasResult{return nil}
	body,err:=boundedRegularBytes(p,"naming-result.json",maxNamingRecord)
	want,e2:=json.Marshal(namingResult{Version:1,AttemptSHA256:digestBytes(raw)})
	if err!=nil||e2!=nil||!bytes.Equal(body,want){return ErrStageRecovery}
	return nil
}
func namingRelative(root,path string)(string,error){
	rel,err:=filepath.Rel(root,path)
	if err!=nil||!validNamingRelative(rel){return "",ErrStageRefused}
	return rel,nil
}
func namingParent(dst *os.Root,rel string)(*os.Root,string,error){
	if !validNamingRelative(rel){return nil,"",ErrStageRefused}
	parent,err:=dst.OpenRoot(".")
	if err!=nil{return nil,"",ErrStageRecovery}
	dir:=filepath.Dir(rel)
	if dir!="."{
		for _,part:=range strings.Split(dir,string(os.PathSeparator)){
			next,err:=openDir(parent,part)
			parent.Close()
			if err!=nil{return nil,"",err}
			parent=next
		}
	}
	return parent,filepath.Base(rel),nil
}
func namingParentLive(r record,dst,parent *os.Root,rel string)bool{
	_,info,err:=directory(filepath.Join(r.GalleryRoot,filepath.Dir(rel)))
	if err!=nil{return false}
	held,err:=parent.Stat(".")
	if err!=nil||!os.SameFile(info,held){return false}
	reopened,_,err:=namingParent(dst,rel)
	if err!=nil{return false}
	defer reopened.Close()
	return sameRelationshipRoot(parent,reopened)
}
func namingFile(ctx context.Context,root *os.Root,name string,f acquisitionplan.VerifiedTransferFile,links uint64)(string,error){
	info,err:=root.Lstat(name)
	if err!=nil||!info.Mode().IsRegular(){return "",ErrStageRecovery}
	file,err:=root.Open(name)
	if err!=nil{return "",ErrStageRecovery}
	defer file.Close()
	actual,err:=file.Stat()
	if err!=nil||!os.SameFile(info,actual)||!exactLinks(file,actual,links){return "",ErrStageRecovery}
	id,ok:=diskFileIdentity(file,actual)
	if !ok{return "",ErrStageRecovery}
	if err:=readCheckedLinks(ctx,root,name,f.Size,f.SHA256,io.Discard,links);err!=nil{return "",err}
	after,err:=root.Lstat(name)
	if err!=nil||!os.SameFile(info,after)||!exactLinks(file,after,links)||after.ModTime()!=info.ModTime(){return "",ErrStageRecovery}
	return id,nil
}
func checkNamingFiles(ctx context.Context,src,dst,parent *os.Root,f acquisitionplan.VerifiedTransferFile,m namingAttempt,old,new bool)error{
	links:=uint64(1)
	if old{links++}
	if new{links++}
	id,err:=namingFile(ctx,src,f.RelativeName,f,links)
	if err!=nil||id!=m.FileIdentity{return ErrStageRecovery}
	for _,v:=range []struct{root *os.Root;name string;exists bool}{
		{dst,m.Old,old},{parent,filepath.Base(m.Final),new},
	}{
		if v.exists{
			id,err:=namingFile(ctx,v.root,v.name,f,links)
			if err!=nil||id!=m.FileIdentity{return ErrStageRecovery}
		}else if _,err:=v.root.Lstat(v.name);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	}
	return nil
}
// The exact single canonical path row is preserved, including its ID.
// No alias adoption, missing-state edits, or blanket updates.
func namingRows(ctx context.Context,g *library.Gallery,r record,f acquisitionplan.VerifiedTransferFile,id int64,rel,absent string,l landingAttempt)(int64,error){
	if l.RestoreImageID>0{return restoredNamingRows(ctx,g,r,f,id,rel,absent,l)}
	tx,err:=g.DB.Read.BeginTx(ctx,&sql.TxOptions{ReadOnly:true})
	if err!=nil{return 0,ErrStageRecovery}
	defer tx.Rollback()
	path:=filepath.Join(r.GalleryRoot,rel)
	var pid int64
	if err:=tx.QueryRowContext(ctx,`SELECT ip.id FROM image_paths ip JOIN images i ON i.id=ip.image_id
		WHERE i.id=? AND i.sha256=? AND i.file_size=? AND i.is_missing=0 AND i.origin=? AND i.canonical_path=?
		AND i.folder_path=? AND ip.path=? AND ip.is_canonical=1`,id,f.SHA256,f.Size,newReceiptOrigin,path,
		gallery.FolderPath(r.GalleryRoot,path),path).Scan(&pid);err!=nil||pid<=0{return 0,ErrStageRecovery}
	for _,q:=range []struct{sql string;arg any;want int}{
		{`SELECT COUNT(*) FROM image_paths WHERE image_id=?`,id,1},
		{`SELECT COUNT(*) FROM image_paths WHERE path=?`,filepath.Join(r.GalleryRoot,absent),0},
	}{
		var n int
		if err:=tx.QueryRowContext(ctx,q.sql,q.arg).Scan(&n);err!=nil||n!=q.want{return 0,ErrStageRecovery}
	}
	if tx.Commit()!=nil||ctx.Err()!=nil{return 0,ErrStageRecovery}
	return pid,nil
}
// Independently verify upstream evidence without assuming the old
// diagnostic path is still canonical. No new receipt is issued here.
func matchesPlanNaming(r record,plan newPublicationPlan,m namingAttempt)bool{
	initial,err:=namingRelative(r.GalleryRoot,plan.Preview.InitialPath)
	final,otherErr:=namingRelative(r.GalleryRoot,plan.Preview.FinalPath)
	return err==nil&&otherErr==nil&&m.Request==plan.Naming&&m.Initial==initial&&m.Final==final&&
		m.InitialSuffixed==plan.Preview.InitialSuffixed&&m.FinalSuffixed==plan.Preview.FinalSuffixed&&m.FileIdentity==plan.FileIdentity
}

func(a *Allocator)namingEvidence(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,r record,e SealedEvidence,p *os.Root,auth namingAuthority,client *clientTagState,restore extrasAuthority,retired bool)(landingAttempt,metadataResult,gallery.ReceivedNamingRequest,error){
	var l landingAttempt;var result metadataResult;var q gallery.ReceivedNamingRequest
	fail:=func(err error)(landingAttempt,metadataResult,gallery.ReceivedNamingRequest,error){return l,result,q,err}
	if g==nil||g.WatcherRunning()||auth==nil||len(e.Files)!=1{return fail(ErrStageRefused)}
	snapshot,err:=a.boundPolicyModeLocked(ctx,grant,retired)
	if err!=nil{return fail(err)}
	rows,err:=newOnlyRowsMode(e,snapshot,true)
	if err!=nil||len(rows)!=1{return fail(ErrStageRefused)}
	l,err=readLandingAttempt(p,r)
	if err!=nil{return fail(err)}
	if restore==nil{
		if !l.NewOnly{return fail(ErrStageRefused)}
	}else{
		if l.NewOnly||l.RestoreImageID<=0||l.RestorePathID<=0||!validDigest(l.RestorePreservationSHA256)||client!=nil{return fail(ErrStageRefused)}
		if _,err:=newOnlyRows(e,snapshot);err!=nil{return fail(err)}
		job,err:=restore(ctx,grant)
		if err!=nil||!job.Empty(){return fail(ErrStageRefused)}
	}
	current,err:=observedDB(r,g)
	if err!=nil||!sameLandingDB(current,l){return fail(ErrStageRecovery)}
	m,err:=readMetadataAttempt(p,l,e)
	if err!=nil{return fail(err)}
	_,hash,err:=acquisitionplan.EncodeApprovedPolicy(snapshot.Transfer,snapshot.Rows)
	if err!=nil||hash!=m.PolicySHA256{return fail(ErrStageRecovery)}
	result,err=readMetadataResult(p,m)
	if err!=nil{return fail(err)}
	v,err:=verifyMappedRow(ctx,g,m.Targets[0],rows[0],result.Rows[0].Rejected)
	if err!=nil||!sameMetadataObservation(v,result.Rows[0]){return fail(ErrStageRecovery)}
	if restore==nil{
		collection,err:=newCollectionProof(ctx,g,p,l,result,rows[0])
		if err!=nil{return fail(err)}
		if err:=verifyNewMetadataStateFull(ctx,g,rows,m,result,client,collection);err!=nil{return fail(err)}
	}else{
		if v.ImageID!=l.RestoreImageID||(l.RestoreRating!=""&&v.Rating!=l.RestoreRating){return fail(ErrStageRecovery)}
		if digest,err:=collectionResultDigest(p);err!=nil||digest!=""{return fail(ErrStageRecovery)}
	}
	relRaw,err:=boundedRegularBytes(p,"relationship-attempt.json",maxRelationshipRecord)
	var rel relationshipAttempt
	if err!=nil||json.Unmarshal(relRaw,&rel)!=nil||rel.ImageID!=v.ImageID||rel.URL!=rows[0].PostURL||rel.ParentURL!=""{return fail(ErrStageRecovery)}
	if err:=validateRelationshipRecords(p,result,true);err!=nil{return fail(err)}
	graph,err:=inspectRelationships(ctx,g,v.ImageID,rel.URL,rel.ParentURL)
	if err!=nil||!sameRelationships(expectedRelationships(rel.Before),graph){return fail(ErrStageRecovery)}
	for _,name:=range []string{"new-receipt.json","duplicate-attempt.json"}{
		if _,err:=p.Lstat(name);!errors.Is(err,os.ErrNotExist){return fail(ErrStageRecovery)}
	}
	q,err=auth(ctx,grant)
	if err!=nil||!validNamingRequest(q)||q.Gallery!=g.Name||q.ReceivedName!=e.Files[0].RelativeName||
		q.Source!=rows[0].SourceLabel||q.PostID!=rows[0].PostID{return fail(ErrStageRefused)}
	plan,_,err:=readPublicationAttempt(p,grant)
	if err!=nil||(plan!=nil&&(restore!=nil||q!=plan.Naming)){return fail(ErrStageRefused)}
	return l,result,q,nil
}
func(a *Allocator)namingCurrent(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,r record,e SealedEvidence,p,src,dst,parent *os.Root,m namingAttempt,auth namingAuthority,client *clientTagState,restore extrasAuthority,retired bool)error{
	l,result,q,err:=a.namingEvidence(ctx,grant,g,r,e,p,auth,client,restore,retired)
	if err!=nil||q!=m.Request||len(result.Rows)!=1||result.Rows[0].ImageID!=m.ImageID{return ErrStageRecovery}
	meta,err:=json.Marshal(result)
	rel,relErr:=boundedRegularBytes(p,"relationship-result.json",maxRelationshipRecord)
	if err!=nil||relErr!=nil||digestBytes(meta)!=m.MetadataSHA256||digestBytes(rel)!=m.RelationshipSHA256{return ErrStageRecovery}
	collection,err:=collectionResultDigest(p)
	if err!=nil||m.CollectionSHA256!=collection{return ErrStageRecovery}
	if restore!=nil{
		if _,err:=dst.Lstat(l.RestorePath);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
		state,err:=restoreStateDigest(ctx,g,m.ImageID)
		if err!=nil||state!=m.RestoreStateSHA256{return ErrStageRecovery}
		job,err:=restore(ctx,grant)
		if err!=nil||!job.Empty(){return ErrStageRefused}
	}else if m.RestoreStateSHA256!=""{return ErrStageRecovery}
	s,err:=a.scope(ctx,grant.Request)
	if err!=nil||!matches(r,s)||g.WatcherRunning()||!a.livePublicationRoots(grant,r,p,src,dst)||!namingParentLive(r,dst,parent,m.Final){return ErrStageRefused}
	current,err:=observedDB(r,g)
	if err!=nil||!sameLandingDB(current,l){return ErrStageRecovery}
	plan,_,err:=readPublicationAttempt(p,grant)
	if err!=nil||(plan!=nil&&!matchesPlanNaming(r,*plan,m)){return ErrStageRecovery}
	if err:=verifyPlanDestinations(r,dst,plan);err!=nil{return err}
	if plan!=nil{if err:=verifyPlannedImageRow(ctx,g,m.ImageID,e.Files[0],plan.Media);err!=nil{return err}}
	if g.Boundary().Check(filepath.Join(r.GalleryRoot,m.Final))!=nil||g.Boundary().Check(filepath.Join(r.GalleryRoot,m.Old))!=nil{return ErrStageRefused}
	if restore!=nil{
		if _,err:=dst.Lstat(l.RestorePath);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
		state,err:=restoreStateDigest(ctx,g,m.ImageID)
		if err!=nil||state!=m.RestoreStateSHA256{return ErrStageRecovery}
	}
	return nil
}
func(a *Allocator)applyNaming(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,auth namingAuthority)error{
	a.mu.Lock();defer a.mu.Unlock()
	return a.applyNamingModeLocked(ctx,grant,g,auth,nil)
}
func(a *Allocator)applyNamingModeLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,auth namingAuthority,restore extrasAuthority)error{
	if err:=a.observeRelationshipsLocked(ctx,grant,g);err!=nil{return err}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return err}
	defer p.Close();defer src.Close();defer dst.Close()
	for _,name:=range []string{"naming-attempt.json","naming-result.json"}{
		if _,err:=p.Lstat(name);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	}
	l,result,q,err:=a.namingEvidence(ctx,grant,g,r,e,p,auth,nil,restore,false)
	if err!=nil{return err}
	f:=e.Files[0]
	preview,err:=gallery.PreviewReceivedNaming(ctx,g.DB,g.Boundary(),result.Rows[0].ImageID,q)
	if err!=nil{return ErrStageRefused}
	plan,_,err:=readPublicationAttempt(p,grant)
	if err!=nil||(plan!=nil&&preview!=plan.Preview){return ErrStageRefused}
	if err:=verifyPlanDestinations(r,dst,plan);err!=nil{return err}
	initial,err:=namingRelative(r.GalleryRoot,preview.InitialPath)
	if err!=nil{return err}
	final,err:=namingRelative(r.GalleryRoot,preview.FinalPath)
	if err!=nil{return err}
	m:=namingAttempt{Version:1,Request:q,ImageID:result.Rows[0].ImageID,Old:finalName(grant,f),Initial:initial,Final:final,
		InitialSuffixed:preview.InitialSuffixed,FinalSuffixed:preview.FinalSuffixed}
	if strings.EqualFold(m.Old,m.Final){return ErrStageRefused}
	if restore!=nil{
		if strings.EqualFold(l.RestorePath,m.Initial)||strings.EqualFold(l.RestorePath,m.Final){return ErrStageRefused}
		m.RestoreStateSHA256,err=restoreStateDigest(ctx,g,m.ImageID)
		if err!=nil{return err}
	}
	parent,_,err:=namingParent(dst,m.Final)
	if err!=nil{return err}
	defer parent.Close()
	m.PathID,err=namingRows(ctx,g,r,f,m.ImageID,m.Old,m.Final,l)
	if err!=nil{return err}
	m.FileIdentity,err=namingFile(ctx,src,f.RelativeName,f,2)
	if err!=nil{return err}
	meta,err:=json.Marshal(result)
	rel,relErr:=boundedRegularBytes(p,"relationship-result.json",maxRelationshipRecord)
	if err!=nil||relErr!=nil{return ErrStageRecovery}
	m.MetadataSHA256=digestBytes(meta);m.RelationshipSHA256=digestBytes(rel)
	m.CollectionSHA256,err=collectionResultDigest(p)
	if err!=nil{return err}
	if err:=checkNamingFiles(ctx,src,dst,parent,f,m,true,false);err!=nil{return err}
	if err:=a.namingCurrent(ctx,grant,g,r,e,p,src,dst,parent,m,auth,nil,restore,false);err!=nil{return err}
	raw,err:=json.Marshal(m)
	if err!=nil||len(raw)>maxNamingRecord{return ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,"naming-attempt.json",raw);err!=nil{return err}
	syncDirectory(p)
	if err:=a.namingCurrent(ctx,grant,g,r,e,p,src,dst,parent,m,auth,nil,restore,false);err!=nil{return err}
	again,err:=gallery.PreviewReceivedNaming(ctx,g.DB,g.Boundary(),m.ImageID,q)
	if err!=nil||again!=preview{return ErrStageRecovery}
	if err:=checkNamingFiles(ctx,src,dst,parent,f,m,true,false);err!=nil{return err}
	// No placeholder or copy fallback. Link to the exact free path;
	// even interruption here leaves the old canonical and private bytes.
	if err:=dst.Link(m.Old,m.Final);err!=nil{return ErrStageRecovery}
	syncDirectory(parent)
	if err:=a.namingCurrent(ctx,grant,g,r,e,p,src,dst,parent,m,auth,nil,restore,false);err!=nil{return err}
	if err:=checkNamingFiles(ctx,src,dst,parent,f,m,true,true);err!=nil{return err}
	pid,err:=namingRows(ctx,g,r,f,m.ImageID,m.Old,m.Final,l)
	if err!=nil||pid!=m.PathID{return ErrStageRecovery}
	oldPath:=filepath.Join(r.GalleryRoot,m.Old);newPath:=filepath.Join(r.GalleryRoot,m.Final)
	err=g.Edit(func(d *db.DB)error{
		return db.InWriteTx(d.Write,func(tx *sql.Tx)error{
			res,err:=tx.ExecContext(ctx,`UPDATE images SET canonical_path=?,folder_path=? WHERE id=? AND canonical_path=?
				AND sha256=? AND file_size=? AND is_missing=0 AND (?=1 OR origin=?)`,newPath,gallery.FolderPath(r.GalleryRoot,newPath),m.ImageID,oldPath,f.SHA256,f.Size,restore!=nil,newReceiptOrigin)
			if err!=nil{return err}
			n,err:=res.RowsAffected();if err!=nil||n!=1{return ErrStageRecovery}
			res,err=tx.ExecContext(ctx,`UPDATE image_paths SET path=? WHERE id=? AND image_id=? AND path=? AND is_canonical=1
				AND NOT EXISTS(SELECT 1 FROM image_paths WHERE path=?)`,newPath,m.PathID,m.ImageID,oldPath,newPath)
			if err!=nil{return err}
			n,err=res.RowsAffected();if err!=nil||n!=1{return ErrStageRecovery}
			return nil
		})
	})
	if err!=nil{return ErrStageRecovery}
	if err:=a.namingCurrent(ctx,grant,g,r,e,p,src,dst,parent,m,auth,nil,restore,false);err!=nil{return err}
	pid,err=namingRows(ctx,g,r,f,m.ImageID,m.Final,m.Old,l)
	if err!=nil||pid!=m.PathID{return ErrStageRecovery}
	if err:=checkNamingFiles(ctx,src,dst,parent,f,m,true,true);err!=nil{return err}
	// Only remove the operation-owned old link after proving the new
	// canonical row AND complete new bytes. No compensating rename.
	if err:=dst.Remove(m.Old);err!=nil{return ErrStageRecovery}
	syncDirectory(dst)
	if err:=checkNamingFiles(ctx,src,dst,parent,f,m,false,true);err!=nil{return err}
	pid,err=namingRows(ctx,g,r,f,m.ImageID,m.Final,m.Old,l)
	if err!=nil||pid!=m.PathID{return ErrStageRecovery}
	if err:=a.namingCurrent(ctx,grant,g,r,e,p,src,dst,parent,m,auth,nil,restore,false);err!=nil{return err}
	body,err:=json.Marshal(namingResult{Version:1,AttemptSHA256:digestBytes(raw)})
	if err!=nil{return ErrStageRecovery}
	if err:=writeOwnerBytes(ctx,p,"naming-result.json",body);err!=nil{return err}
	syncDirectory(p)
	return a.observeNamingModeLocked(ctx,grant,g,auth,nil,restore)
}
func(a *Allocator)observeNaming(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,auth namingAuthority)error{
	a.mu.Lock();defer a.mu.Unlock()
	return a.observeNamingLocked(ctx,grant,g,auth)
}
func(a *Allocator)observeNamingLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,auth namingAuthority)error{
	return a.observeNamingWithClientLocked(ctx,grant,g,auth,nil)
}
func(a *Allocator)observeNamingWithClientLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,auth namingAuthority,client *clientTagState)error{
	return a.observeNamingModeLocked(ctx,grant,g,auth,client,nil)
}
func(a *Allocator)observeNamingModeLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,auth namingAuthority,client *clientTagState,restore extrasAuthority)error{
	return a.observeNamingAfterReleaseLocked(ctx,grant,g,auth,client,restore,false,false)
}
func(a *Allocator)observeNamingAfterReleaseLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,auth namingAuthority,client *clientTagState,restore extrasAuthority,released,retired bool)error{
	r,e,p,src,dst,err:=a.publicationRootsMode(ctx,grant,retired)
	if err!=nil{return err}
	defer p.Close();defer src.Close();defer dst.Close()
	l,result,q,err:=a.namingEvidence(ctx,grant,g,r,e,p,auth,client,restore,retired)
	if err!=nil{return err}
	if err:=validateNamingRecords(p,r,e,l,result,true);err!=nil{return err}
	m,_,err:=readNamingAttempt(p,r,e,result)
	if err!=nil||m.Request!=q{return ErrStageRecovery}
	parent,_,err:=namingParent(dst,m.Final)
	if err!=nil{return err}
	defer parent.Close()
	if err:=a.namingCurrent(ctx,grant,g,r,e,p,src,dst,parent,m,auth,client,restore,retired);err!=nil{return err}
	f:=e.Files[0]
	pid,err:=namingRows(ctx,g,r,f,m.ImageID,m.Final,m.Old,l)
	if err!=nil||pid!=m.PathID{return ErrStageRecovery}
	if released{
		if err:=verifyReleasedNamingFiles(ctx,src,dst,parent,f,m);err!=nil{return err}
	}else if err:=checkNamingFiles(ctx,src,dst,parent,f,m,false,true);err!=nil{return err}
	return a.namingCurrent(ctx,grant,g,r,e,p,src,dst,parent,m,auth,client,restore,retired)
}
