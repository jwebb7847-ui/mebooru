// Package windowregistry stores exact owner-issued job-window grants
// and custody reconciliation. It does not evaluate blacklist policy,
// grant online consent, or replace the exclusive profile-owner fence.
package windowregistry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

var ErrWindow=errors.New("managed window not authorized or requires reconciliation")
const maxRecord=64<<10

type Grant struct{
	Request workerproto.MetadataPlanRequest
	OwnerGeneration string
	PolicyGeneration uint64
	GalleryIdentity string
	IntentID string
	Identity acquisitionplan.MetadataIdentity
}
type Outcome struct{
	Grant Grant
	Kind string // committed, no_transfer, abandoned
	PolicySHA256 string
	PlanSHA256 string
}
type Registry struct{root *os.Root}
func Open(path string)(*Registry,error){
	if !filepath.IsAbs(path)||filepath.Clean(path)!=path{return nil,ErrWindow}
	canonical,err:=filepath.EvalSymlinks(path);if err!=nil||canonical!=path{return nil,ErrWindow}
	info,err:=os.Lstat(path);if err!=nil||!info.IsDir(){return nil,ErrWindow}
	root,err:=os.OpenRoot(path);if err!=nil{return nil,err}
	actual,err:=root.Stat(".");if err!=nil||!os.SameFile(info,actual){root.Close();return nil,ErrWindow}
	return &Registry{root},nil
}
func(r *Registry)Close()error{return r.root.Close()}
func validHex(s string,n int)bool{b,err:=hex.DecodeString(s);return err==nil&&len(b)==n&&hex.EncodeToString(b)==s}
func valid(g Grant)bool{
	if !workerproto.ValidMetadataPlan(g.Request)||g.Request.Kind!=workerproto.MetadataStrictResolve||
		!validHex(g.OwnerGeneration,32)||g.OwnerGeneration==strings.Repeat("0",64)||g.PolicyGeneration==0||g.IntentID!=""&&!workerproto.ValidQueueIntentID(g.IntentID)||
		len(g.GalleryIdentity)<1||len(g.GalleryIdentity)>1024||!utf8.ValidString(g.GalleryIdentity)||strings.ContainsFunc(g.GalleryIdentity,unicode.IsControl){return false}
	// Reconstruct the credential-free qualified config's actual digest
	// and validate the exact runtime version/artifact/manifest identity.
	cfg:=acquisitionplan.MetadataConfig{Generation:g.Identity.Config.Generation,Source:g.Request.Source}
	id,err:=cfg.Identity();if err!=nil||id!=g.Identity.Config{return false}
	_,err=acquisitionplan.NewMetadataSequence(g.Request,g.Identity.Runtime,cfg)
	return err==nil
}
func name(q workerproto.MetadataPlanRequest,suffix string)string{return strconv.FormatInt(q.JobID,10)+"."+suffix}
func(r *Registry) read(path string,v any)error{
	i,err:=r.root.Lstat(path);if err!=nil{return err}
	if !i.Mode().IsRegular()||i.Size()<1||i.Size()>maxRecord{return ErrWindow}
	f,err:=r.root.Open(path);if err!=nil{return err};defer f.Close()
	actual,err:=f.Stat();if err!=nil||!os.SameFile(i,actual){return ErrWindow}
	raw,err:=io.ReadAll(io.LimitReader(f,maxRecord+1));if err!=nil||int64(len(raw))!=i.Size()||json.Unmarshal(raw,v)!=nil{return ErrWindow}
	b,err:=json.Marshal(v);if err!=nil||!bytes.Equal(raw,b){return ErrWindow};return nil
}
func(r *Registry) absent(path string)bool{_,err:=r.root.Lstat(path);return errors.Is(err,os.ErrNotExist)}
func(r *Registry) write(ctx context.Context,path string,v any)error{
	if ctx.Err()!=nil{return ctx.Err()}
	raw,err:=json.Marshal(v);if err!=nil||len(raw)>maxRecord{return ErrWindow}
	f,err:=r.root.OpenFile(path,os.O_CREATE|os.O_EXCL|os.O_WRONLY,0600);if err!=nil{return err}
	n,err:=f.Write(raw);if err==nil&&n!=len(raw){err=io.ErrShortWrite}
	if err==nil{err=f.Sync()}
	if err=errors.Join(err,f.Close());err!=nil{return err}
	// Sync directory where supported; on Windows the synced file remains
	// the durable witness. Partial records refuse rather than replay.
	if dir,e:=r.root.Open(".");e==nil{_ = dir.Sync();_ = dir.Close()}
	return ctx.Err()
}

// Register is called only by the profile owner after independently
// validating the persisted job and current policy. Existing IDs cannot
// be replaced; retry of an uncertain job requires explicit reconciliation.
func(r *Registry)Register(ctx context.Context,g Grant)error{
	if !valid(g)||!r.submissionBound(g){return ErrWindow}
	q:=g.Request
	for _,s:=range []string{"grant","claimed","outcome","settled"}{if !r.absent(name(q,s)){return ErrWindow}}
	return r.write(ctx,name(q,"grant"),g)
}
func(r *Registry)Observe(q workerproto.MetadataPlanRequest,generation string)(Grant,error){
	var g Grant
	if !workerproto.ValidMetadataPlan(q)||r.read(name(q,"grant"),&g)!=nil||!valid(g)||g.Request!=q||g.OwnerGeneration!=generation||!r.submissionBound(g){return Grant{},ErrWindow}
	return g,nil
}

// Claim is a one-use custody transition. Only this custodian generation
// may claim the exact registered request; a lost acknowledgement cannot
// create a fresh grant from the same job ID.
func(r *Registry)Claim(ctx context.Context,q workerproto.MetadataPlanRequest,generation string)(Grant,error){
	g,err:=r.Observe(q,generation)
	if err!=nil||!r.absent(name(q,"outcome"))||!r.absent(name(q,"settled")){return Grant{},ErrWindow}
	if err:=r.write(ctx,name(q,"claimed"),g);err!=nil{return Grant{},err}
	return g,nil
}
func(r *Registry)Current(g Grant)bool{
	var claimed Grant
	observed,err:=r.Observe(g.Request,g.OwnerGeneration)
	return err==nil&&observed==g&&r.read(name(g.Request,"claimed"),&claimed)==nil&&claimed==g&&r.absent(name(g.Request,"settled"))
}
func validOutcome(v Outcome)bool{
	if !valid(v.Grant){return false}
	switch v.Kind{
	case "committed":return validHex(v.PolicySHA256,32)&&validHex(v.PlanSHA256,32)
	case "no_transfer","abandoned":return v.PolicySHA256==""&&v.PlanSHA256==""
	default:return false
	}
}
// RecordOutcome is owner-only. The caller must verify actual durable
// receipt evidence or explicit no-transfer/abandonment authority.
func(r *Registry)RecordOutcome(ctx context.Context,v Outcome)error{
	if !validOutcome(v)||!r.Current(v.Grant){return ErrWindow}
	return r.write(ctx,name(v.Grant.Request,"outcome"),v)
}
// Settle consumes independent owner outcome only after the runtime
// broker proves no live handles for the window. It does not remove
// records or grant permission for another job.
func(r *Registry)Settle(ctx context.Context,g Grant,phase int,failed bool)error{
	var v Outcome
	if !r.Current(g)||r.read(name(g.Request,"outcome"),&v)!=nil||!validOutcome(v)||v.Grant!=g{return ErrWindow}
	switch v.Kind{
	case "committed":if failed||phase!=6{return ErrWindow}
	case "no_transfer":if failed||phase!=2{return ErrWindow}
	case "abandoned":if phase<0||phase>6{return ErrWindow}
	}
	return r.write(ctx,name(g.Request,"settled"),v)
}


// ObserveSettlement requires a positive exact durable record; absence
// or invalidity of the current grant alone does not prove settlement.
func(r *Registry)ObserveSettlement(g Grant)(Outcome,error){
	var zero Outcome
	got,err:=r.Observe(g.Request,g.OwnerGeneration)
	var claimed Grant
	var outcome,settled Outcome
	if err!=nil||got!=g||r.read(name(g.Request,"claimed"),&claimed)!=nil||claimed!=g||
		r.read(name(g.Request,"outcome"),&outcome)!=nil||!validOutcome(outcome)||outcome.Grant!=g||
		r.read(name(g.Request,"settled"),&settled)!=nil||settled!=outcome{return zero,ErrWindow}
	return settled,nil
}


// Admitting is stronger than Current: an owner outcome ends new
// acquisition admission but the original claim remains readable until
// the custodian positively settles it.
func(r *Registry)Admitting(g Grant)bool{
	return r.Current(g)&&r.absent(name(g.Request,"outcome"))
}


// Submission is durable user intent, not a network grant. It can be
// created offline, before the worker has assigned a numeric queue ID.
// Owner-generated ID is private correlation, not a credential or
// same-user OS security boundary.
type Submission struct{
	Command workerproto.QueueCommand
	GalleryIdentity string
}
func validSubmission(v Submission)bool{
	if !workerproto.ValidQueueIntentID(v.Command.IntentID)||len(v.GalleryIdentity)<1||len(v.GalleryIdentity)>1024||
		!utf8.ValidString(v.GalleryIdentity)||strings.ContainsFunc(v.GalleryIdentity,unicode.IsControl){return false}
	_,err:=workerproto.EncodeQueueCommand(workerproto.QueueEnqueue,v.Command)
	return err==nil
}
func submissionPath(id,suffix string)string{return "submission."+id+"."+suffix}
func(r *Registry) NewSubmission(ctx context.Context,cmd workerproto.QueueCommand,stableGallery string)(Submission,error){
	if cmd.IntentID!=""{return Submission{},ErrWindow}
	var id [16]byte
	if _,err:=rand.Read(id[:]);err!=nil{return Submission{},err}
	cmd.IntentID=hex.EncodeToString(id[:])
	v:=Submission{Command:cmd,GalleryIdentity:stableGallery}
	if !validSubmission(v)||!r.absent(submissionPath(cmd.IntentID,"bound")){return Submission{},ErrWindow}
	if err:=r.write(ctx,submissionPath(cmd.IntentID,"intent"),v);err!=nil{return Submission{},err}
	return v,nil
}
func(r *Registry) ObserveSubmission(id string)(Submission,error){
	var v Submission
	if !workerproto.ValidQueueIntentID(id)||r.read(submissionPath(id,"intent"),&v)!=nil||!validSubmission(v)||v.Command.IntentID!=id{return Submission{},ErrWindow}
	return v,nil
}
func matchesSubmission(v Submission,g Grant)bool{
	q:=g.Request;c:=v.Command
	return valid(g)&&validSubmission(v)&&g.IntentID==c.IntentID&&g.GalleryIdentity==v.GalleryIdentity&&q.URL==c.URL&&q.Gallery==c.Gallery&&
		q.Source==workerproto.StrictQueueSource(c.URL)&&q.First==1&&uint64(q.Last)==uint64(c.MaxItems)
}
func(r *Registry) submissionBound(g Grant)bool{
	if g.IntentID==""{return true} // isolated pre-submission fixtures only
	v,err:=r.ObserveSubmission(g.IntentID)
	var bound Grant
	return err==nil&&matchesSubmission(v,g)&&r.read(submissionPath(g.IntentID,"bound"),&bound)==nil&&bound==g
}
// BindSubmission consumes exactly one owner intent for exactly one
// running job/generation. An incomplete bind or lost response is
// intentionally ambiguous; never create a second grant automatically.
func(r *Registry) BindSubmission(ctx context.Context,g Grant)error{
	v,err:=r.ObserveSubmission(g.IntentID)
	if err!=nil||!matchesSubmission(v,g){return ErrWindow}
	if err:=r.write(ctx,submissionPath(g.IntentID,"bound"),g);err!=nil{return err}
	return r.Register(ctx,g)
}
