package taxonomy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monloader/internal/kwdict"
)

type managedTaxonomyTransportFunc func(*http.Request)(*http.Response,error)
func(f managedTaxonomyTransportFunc)RoundTrip(r *http.Request)(*http.Response,error){return f(r)}

func managedTaxonomyFixture()(workerproto.MetadataPlanRequest,[]byte){
	return workerproto.MetadataPlanRequest{JobID:7,Gallery:"A",Kind:workerproto.MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/images/1",First:1,Last:1},
		[]byte(`[[3,"https://example.invalid/1.jpg",{"id":1,"category":"derpibooru","subcategory":"post","tags":["safe","dashie"],"tag_ids":[1,24568]}]]`)
}
func TestManagedTaxonomyExactWindowAndAlias(t *testing.T){
	ctx:=context.Background()
	q,body:=managedTaxonomyFixture()
	gate:=workerproto.NewDrainGate(ctx)
	calls:=0
	transport:=managedTaxonomyTransportFunc(func(r *http.Request)(*http.Response,error){
		calls++
		var raw string
		switch r.URL.Path{
		case "/api/v1/json/search/tags":
			if r.URL.Query().Get("q")!="id:1 || id:24568"&&r.URL.Query().Get("q")!="id:24568 || id:1"{t.Fatalf("unexpected search %s",r.URL.RawQuery)}
			raw=`{"tags":[{"id":1,"name":"safe","category":"rating","aliased_tag":null,"aliases":[]},{"id":24568,"name":"dashie","slug":"dashie","category":null,"aliased_tag":"rainbow+dash","aliases":[]}]}`
		case "/api/v1/json/tags/rainbow+dash":
			raw=`{"tag":{"id":39229,"name":"rainbow dash","slug":"rainbow+dash","category":"character","aliased_tag":null,"aliases":["dashie"]}}`
		default:t.Fatalf("unexpected path: %s",r.URL.Path)
		}
		return &http.Response{StatusCode:200,Body:io.NopCloser(strings.NewReader(raw)),Header:make(http.Header)},nil
	})
	makeWindow:=func()*ManagedTaxonomyWindow{
		w,err:=NewManagedTaxonomyWindow(q,"post",body,gate,gate.CurrentGeneration(),func(context.Context,workerproto.MetadataPlanRequest)bool{return true},transport)
		if err!=nil{t.Fatal(err)}
		return w
	}
	w:=makeWindow()
	if out,err:=w.Run(ctx,q);out!=nil||!errors.Is(err,workerproto.ErrOffline)||calls!=0{t.Fatal("offline admission")}
	if err:=gate.Open();err!=nil{t.Fatal(err)}
	if _,err:=w.Run(ctx,q);err==nil{t.Fatal("offline grant replay")}
	w=makeWindow()
	bad:=q;bad.Gallery="B"
	if _,err:=w.Run(ctx,bad);err==nil||calls!=0{t.Fatal("widened window")}
	out,err:=w.Run(ctx,q)
	if err!=nil||len(out)!=1||calls!=2{t.Fatalf("taxonomy result %v %v calls=%d",out,err,calls)}
	native:=out[0].Native["tags"].([]any)
	tags:=out[0].Interpreted[kwdict.InterpretedTagsKey].([]kwdict.InterpretedTag)
	if native[1]!="dashie"||len(tags)!=1||tags[0].Name!="rainbow dash"||tags[0].Category!="character"{t.Fatalf("alias or raw evidence changed: %v %v",native,tags)}
	if _,err:=w.Run(ctx,q);err==nil{t.Fatal("replay")}
	if err:=gate.Drain(ctx);err!=nil{t.Fatal(err)}
}
func TestManagedTaxonomyDrainAndRedirectRefusal(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second)
	defer cancel()
	q,body:=managedTaxonomyFixture()
	gate:=workerproto.NewDrainGate(ctx)
	if err:=gate.Open();err!=nil{t.Fatal(err)}
	entered,revoked,release:=make(chan struct{}),make(chan struct{}),make(chan struct{})
	var calls atomic.Int32
	w,err:=NewManagedTaxonomyWindow(q,"post",body,gate,gate.CurrentGeneration(),func(context.Context,workerproto.MetadataPlanRequest)bool{return true},
		managedTaxonomyTransportFunc(func(r *http.Request)(*http.Response,error){
			calls.Add(1);close(entered);<-r.Context().Done();close(revoked)
			select{case <-release:case <-ctx.Done():}
			return nil,r.Context().Err()
		}))
	if err!=nil{t.Fatal(err)}
	run:=make(chan error,1)
	go func(){out,err:=w.Run(ctx,q);if out!=nil{run<-errors.New("canceled taxonomy published");return};run<-err}()
	wait:=func(c <-chan struct{}){t.Helper();select{case <-c:case <-ctx.Done():t.Fatal(ctx.Err())}}
	wait(entered)
	drain:=make(chan error,1)
	go func(){drain<-gate.Drain(ctx)}()
	wait(revoked)
	select{case err:=<-drain:t.Fatalf("early drain %v",err);default:}
	close(release)
	select{case err:=<-run:if err==nil{t.Fatal("canceled request accepted")};case <-ctx.Done():t.Fatal(ctx.Err())}
	select{case err:=<-drain:if err!=nil{t.Fatal(err)};case <-ctx.Done():t.Fatal(ctx.Err())}
	if calls.Load()!=1{t.Fatal("requests after cancellation")}
	if err:=gate.Open();err!=nil{t.Fatal(err)}
	w,err=NewManagedTaxonomyWindow(q,"post",body,gate,gate.CurrentGeneration(),func(context.Context,workerproto.MetadataPlanRequest)bool{return true},
		managedTaxonomyTransportFunc(func(r *http.Request)(*http.Response,error){
			calls.Add(1)
			return &http.Response{StatusCode:302,Header:http.Header{"Location":[]string{"https://example.invalid/"}},Body:io.NopCloser(strings.NewReader(""))},nil
		}))
	if err!=nil{t.Fatal(err)}
	if out,err:=w.Run(ctx,q);out!=nil||err==nil||calls.Load()!=2{t.Fatalf("redirect followed: %v %v calls=%d",out,err,calls.Load())}
}

type unconfirmedTaxonomyBody struct{io.Reader}
func(unconfirmedTaxonomyBody)Close()error{return errors.New("fixture close failure")}

func TestManagedTaxonomyRejectsStaleGenerationAndUnconfirmedClose(t *testing.T){
	ctx:=context.Background()
	q,body:=managedTaxonomyFixture()
	gate:=workerproto.NewDrainGate(ctx)
	if err:=gate.Open();err!=nil{t.Fatal(err)}
	old:=gate.CurrentGeneration()
	calls:=0
	transport:=managedTaxonomyTransportFunc(func(*http.Request)(*http.Response,error){
		calls++
		return &http.Response{StatusCode:200,Header:make(http.Header),Body:unconfirmedTaxonomyBody{strings.NewReader("{}")}},nil
	})
	scope:=func(context.Context,workerproto.MetadataPlanRequest)bool{return true}
	if err:=gate.Drain(ctx);err!=nil{t.Fatal(err)}
	if err:=gate.Open();err!=nil{t.Fatal(err)}
	stale,err:=NewManagedTaxonomyWindow(q,"post",body,gate,old,scope,transport)
	if err!=nil{t.Fatal(err)}
	if out,err:=stale.Run(ctx,q);out!=nil||!errors.Is(err,workerproto.ErrOffline)||calls!=0{t.Fatal("stale metadata generation admitted")}
	fresh,err:=NewManagedTaxonomyWindow(q,"post",body,gate,gate.CurrentGeneration(),scope,transport)
	if err!=nil{t.Fatal(err)}
	if out,err:=fresh.Run(ctx,q);out!=nil||!errors.Is(err,workerproto.ErrDrainUnconfirmed)||calls!=1{t.Fatalf("unconfirmed close: %v %v calls=%d",out,err,calls)}
	if !errors.Is(gate.Drain(ctx),workerproto.ErrDrainUnconfirmed)||gate.Open()==nil{t.Fatal("unconfirmed HTTP cleanup reopened admission")}
}

func TestManagedTaxonomyRejectsAmbiguousSourceJSON(t *testing.T){
	if validateManagedTagJSON([]byte{'{','"','x','"',':','"',0xff,'"','}'}){t.Fatal("invalid UTF-8 normalized into source evidence")}
	for _,bad:=range []string{`{"tags":[{"id":1,"id":2}]}`,`{"tags":[{"id":1,"ID":2}]}`,`{"tags":[]} {"tag":{}}`,`null`,`{"tags":[`+strings.Repeat("[",33)+strings.Repeat("]",33)+`]}`}{
		if validateManagedTagJSON([]byte(bad)){t.Fatalf("ambiguous response accepted %s",bad)}
	}
	if !validateManagedTagJSON([]byte(`{"tags":[{"id":1,"name":"fixture","category":null,"aliases":[],"aliased_tag":null}]}`)){t.Fatal("valid source response refused")}
}
