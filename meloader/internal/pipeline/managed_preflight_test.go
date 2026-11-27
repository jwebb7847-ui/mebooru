package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/mapping"
)

func managedPreflightFixture(t *testing.T,rule string)(*ManagedPreflight,*config.Provider,*workerproto.DrainGate,workerproto.MetadataPlanRequest,*uint64){
	t.Helper()
	ctx:=context.Background()
	gate:=workerproto.NewDrainGate(ctx)
	if err:=gate.Open();err!=nil{t.Fatal(err)}
	cfg:=config.Default()
	cfg.Tags.StrictBlacklist=true
	cfg.Tags.Blacklist=[]string{rule}
	provider:=config.NewProvider(cfg)
	mapper,err:=mapping.New(provider,"")
	if err!=nil{t.Fatal(err)}
	q:=workerproto.MetadataPlanRequest{JobID:1,Gallery:"A",Kind:workerproto.MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/search?q=fixture",First:1,Last:2}
	body:=[]byte(`[[3,"https://example.invalid/41.jpg",{"id":41,"category":"derpibooru","subcategory":"search","tags":["safe","dashie","violence"],"tag_ids":[1,24568,3]}],[3,"https://example.invalid/42.jpg",{"id":42,"category":"derpibooru","subcategory":"search","tags":["explicit","dashie"],"tag_ids":[2,24568]}]]`)
	transport:=managedEvidenceTransport(func(r *http.Request)(*http.Response,error){
		var data string
		switch r.URL.Path{
		case "/api/v1/json/search/tags":
			data=`{"tags":[{"id":1,"name":"safe","category":"rating","aliased_tag":null,"aliases":[]},{"id":2,"name":"explicit","category":"rating","aliased_tag":null,"aliases":[]},{"id":3,"name":"violence","category":null,"aliased_tag":null,"aliases":[]},{"id":24568,"name":"dashie","slug":"dashie","category":null,"aliased_tag":"rainbow+dash","aliases":[]}]}`
		case "/api/v1/json/tags/rainbow+dash":
			data=`{"tag":{"id":39229,"name":"rainbow dash","slug":"rainbow+dash","category":"character","aliased_tag":null,"aliases":["dashie"]}}`
		default:t.Fatal("unexpected source request")
		}
		return &http.Response{StatusCode:200,Header:make(http.Header),Body:io.NopCloser(strings.NewReader(data))},nil
	})
	w,err:=newManagedEvidenceWindow(q,"search",body,gate,gate.CurrentGeneration(),func(context.Context,workerproto.MetadataPlanRequest)bool{return true},mapper,transport)
	if err!=nil{t.Fatal(err)}
	c:=acquisitionplan.MetadataConfig{Generation:1,Source:q.Source}
	ci,err:=c.Identity()
	if err!=nil{t.Fatal(err)}
	id:=acquisitionplan.MetadataIdentity{Runtime:acquisitionplan.RuntimeIdentity{Generation:1,Version:acquisitionplan.StrictQualifiedVersion,ArtifactSHA256:strings.Repeat("a",64),ManifestSHA256:strings.Repeat("b",64)},Config:ci}
	generation:=uint64(1)
	p,err:=newManagedPreflight(w,id,c,generation,func(context.Context)(acquisitionplan.MetadataIdentity,uint64,error){return id,generation,nil})
	if err!=nil{t.Fatal(err)}
	return p,provider,gate,q,&generation
}

func TestManagedPreflightAliasBlacklistAndApprovedNativeManifest(t *testing.T){
	for _,rule:=range []string{"character:rainbow_dash violence","character:dashie violence"}{
		t.Run(rule,func(t *testing.T){
			ctx:=context.Background()
			p,_,gate,q,_:=managedPreflightFixture(t,rule)
			result,err:=p.Run(ctx,q)
			if err!=nil||len(result.BlockedIDs)!=1||result.BlockedIDs[0]!="41"||result.Approval==nil{t.Fatalf("preflight: %+v %v",result,err)}
			a:=result.Approval
			bad:=a.Request();bad.Metadata.Gallery="B"
			if _,_,err:=a.policyEvidence(ctx,bad);err==nil{t.Fatal("foreign policy evidence")}
			body,digest,err:=a.policyEvidence(ctx,a.Request())
			if err!=nil{t.Fatal(err)}
			sum:=sha256.Sum256(body)
			if digest!=hex.EncodeToString(sum[:]){t.Fatal("projection digest")}
			var projection approvedPolicySnapshot
			if json.Unmarshal(body,&projection)!=nil||projection.Transfer!=a.Request()||len(projection.Rows)!=1{t.Fatalf("projection %s",body)}
			r:=projection.Rows[0]
			if r.PostID!="42"||r.Rating!="explicit"||!slices.Contains(r.Tags,"rating:explicit")||!slices.Contains(r.Tags,"character:rainbow_dash")||len(r.Taxonomy)!=1||r.Taxonomy[0].Name!="rainbow dash"{t.Fatalf("mapped evidence %+v",r)}
			body[0]='!'
			if again,hash,err:=a.policyEvidence(ctx,a.Request());err!=nil||hash!=digest||again[0]!='{'{t.Fatal("mutable policy snapshot")}
			if _,_,_,err:=a.admit(ctx,bad);err==nil{t.Fatal("wrong gallery")}
			_,raw,finish,err:=a.admit(ctx,a.Request())
			if err!=nil{t.Fatal(err)}
			defer finish(nil)
			actualPolicy,actualHash,err:=a.policyEvidence(ctx,a.Request())
			if err!=nil{t.Fatal(err)}
			if _,err:=acquisitionplan.DecodeApprovedPolicy(actualPolicy,a.Request(),actualHash,raw);err!=nil{t.Fatal("shared policy manifest verification",err)}
			var rows map[string]map[string]json.RawMessage
			if json.Unmarshal(raw,&rows)!=nil||len(rows)!=1||rows["41"]!=nil||rows["42"]==nil||string(rows["42"]["tags"])!=`["explicit","dashie"]`{t.Fatalf("bad manifest %s",raw)}
			if _,_,_,err:=a.admit(ctx,a.Request());err==nil{t.Fatal("replayed approval")}
			finish(nil)
			if err:=gate.Drain(ctx);err!=nil{t.Fatal(err)}
		})
	}
}

func TestManagedPreflightAllBlockedAndStalePolicy(t *testing.T){
	ctx:=context.Background()
	p,_,_,q,_:=managedPreflightFixture(t,"character:rainbow_dash")
	result,err:=p.Run(ctx,q)
	if err!=nil||result.Approval!=nil||len(result.BlockedIDs)!=2{t.Fatalf("all blocked: %+v %v",result,err)}
	p,provider,_,q,_:=managedPreflightFixture(t,"violence")
	if err:=provider.Update(func(c *config.Config)error{c.Tags.Blacklist=[]string{"fixture"};return nil});err!=nil{t.Fatal(err)}
	if r,err:=p.Run(ctx,q);r.Approval!=nil||err==nil{t.Fatal("changed config authorized preflight")}
	for _,mode:=range []string{"policy","config","offline"}{
		t.Run(mode,func(t *testing.T){
			p,provider,gate,q,generation:=managedPreflightFixture(t,"violence")
			result,err:=p.Run(ctx,q)
			if err!=nil||result.Approval==nil{t.Fatalf("preflight: %v",err)}
			switch mode{
			case "policy":*generation++
			case "config":
				if err:=provider.Update(func(c *config.Config)error{c.Tags.Blacklist=[]string{"explicit"};return nil});err!=nil{t.Fatal(err)}
			case "offline":
				if err:=gate.Drain(ctx);err!=nil{t.Fatal(err)}
				if err:=gate.Open();err!=nil{t.Fatal(err)}
			}
			if b,_,err:=result.Approval.policyEvidence(ctx,result.Approval.Request());b!=nil||err==nil{t.Fatal("stale policy evidence published")}
			_,raw,finish,err:=result.Approval.admit(ctx,result.Approval.Request())
			if finish!=nil{finish(nil)}
			if raw!=nil||err==nil{t.Fatal("stale approval admitted")}
			if mode=="offline"&&!errors.Is(err,workerproto.ErrOffline){t.Fatal(err)}
		})
	}
}
