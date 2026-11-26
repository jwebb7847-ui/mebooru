package acquisitionplan

import (
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

func sequenceFixture(t *testing.T) (*MetadataSequence, workerproto.MetadataPlanRequest, workerproto.MetadataPlanRequest, MetadataIdentity) {
	t.Helper()
	q:=workerproto.MetadataPlanRequest{JobID:8,Gallery:"A",Kind:workerproto.MetadataStrictResolve,Source:"danbooru",URL:"https://danbooru.donmai.us/posts/1",First:1,Last:1}
	runtime:=RuntimeIdentity{Generation:1,Version:StrictQualifiedVersion,ArtifactSHA256:strings.Repeat("a",64),ManifestSHA256:strings.Repeat("b",64)}
	cfg:=MetadataConfig{Generation:2,Source:q.Source}
	id,err:=cfg.Identity()
	if err!=nil{t.Fatal(err)}
	s,err:=NewMetadataSequence(q,runtime,cfg)
	if err!=nil{t.Fatal(err)}
	classify:=q
	classify.Kind=workerproto.MetadataClassify
	classify.First,classify.Last=0,0
	return s,q,classify,MetadataIdentity{Runtime:runtime,Config:id}
}
func validClassification(q workerproto.MetadataPlanRequest,id MetadataIdentity) ClassificationCompletion {
	return ClassificationCompletion{Request:q,Observed:id,Cleaned:true,Stdout:"Category / Subcategory / Site\n\"danbooru\" / \"post\" / \"Danbooru\"\n"}
}
func TestMetadataSequenceSingleUse(t *testing.T) {
	s,q,c,id:=sequenceFixture(t)
	a,err:=s.BeginClassification(c,id)
	want:=[]string{"--config-ignore","-o","timeout=20","-o","retries=2","-o","extractor.danbooru.metadata=false","-E","--",q.URL}
	if err!=nil||!slices.Equal(a,want){t.Fatalf("classify %v %v",a,err)}
	if err:=s.AcceptClassification(validClassification(c,id));err!=nil{t.Fatal(err)}
	a,err=s.BeginResolve(q,id)
	if err!=nil||!slices.Equal(a[:7],want[:7])||!slices.Equal(a[len(a)-4:],[]string{"--post-range","1-1","--",q.URL}){t.Fatalf("resolve %v %v",a,err)}
	if a,err:=s.BeginResolve(q,id);a!=nil||err==nil{t.Fatal("replayed resolve")}
}
func TestMetadataSequenceRefusesUntrustedCompletion(t *testing.T) {
	for _,change:=range []func(*ClassificationCompletion){
		func(r *ClassificationCompletion){r.Cleaned=false},
		func(r *ClassificationCompletion){r.ExitCode=1},
		func(r *ClassificationCompletion){r.Stderr="secret"},
		func(r *ClassificationCompletion){r.Request.JobID++},
		func(r *ClassificationCompletion){r.Observed.Runtime.Generation++},
		func(r *ClassificationCompletion){r.Observed.Config.Generation++},
		func(r *ClassificationCompletion){r.Stdout=strings.ReplaceAll(r.Stdout,"post","pool")},
		func(r *ClassificationCompletion){r.Stdout=strings.ReplaceAll(r.Stdout,"danbooru","e621")},
		func(r *ClassificationCompletion){r.Stdout+=r.Stdout},
		func(r *ClassificationCompletion){r.Stdout=strings.Repeat("x",maxClassificationBytes+1)},
	}{
		s,q,c,id:=sequenceFixture(t)
		if _,err:=s.BeginClassification(c,id);err!=nil{t.Fatal(err)}
		r:=validClassification(c,id);change(&r)
		if err:=s.AcceptClassification(r);err==nil{t.Fatal("accepted untrusted completion")}
		if err:=s.AcceptClassification(validClassification(c,id));err==nil{t.Fatal("refused sequence reopened")}
		if a,err:=s.BeginResolve(q,id);a!=nil||err==nil{t.Fatal("resolve after failure")}
	}
}
func TestMetadataSequenceIdentityAndRevocation(t *testing.T) {
	for _,change:=range []func(*MetadataIdentity){
		func(i *MetadataIdentity){i.Runtime.ArtifactSHA256=strings.Repeat("c",64)},
		func(i *MetadataIdentity){i.Runtime.ManifestSHA256=strings.Repeat("c",64)},
		func(i *MetadataIdentity){i.Runtime.Version="1.32.21"},
		func(i *MetadataIdentity){i.Runtime.Generation++},
		func(i *MetadataIdentity){i.Config.Generation++},
		func(i *MetadataIdentity){i.Config.SHA256=strings.Repeat("c",64)},
	}{
		s,q,c,id:=sequenceFixture(t)
		if _,err:=s.BeginClassification(c,id);err!=nil{t.Fatal(err)}
		if err:=s.AcceptClassification(validClassification(c,id));err!=nil{t.Fatal(err)}
		changed:=id;change(&changed)
		if a,err:=s.BeginResolve(q,changed);a!=nil||err==nil{t.Fatal("changed identity accepted")}
		if a,err:=s.BeginResolve(q,id);a!=nil||err==nil{t.Fatal("old identity reactivated")}
	}
	s,q,c,id:=sequenceFixture(t)
	if a,err:=s.BeginResolve(q,id);a!=nil||err==nil{t.Fatal("resolve before classification")}
	s,q,c,id=sequenceFixture(t)
	if _,err:=s.BeginClassification(c,id);err!=nil{t.Fatal(err)}
	s.Invalidate()
	if err:=s.AcceptClassification(validClassification(c,id));err==nil{t.Fatal("revoked classification accepted")}
	s,q,c,id=sequenceFixture(t)
	if _,err:=s.BeginClassification(c,id);err!=nil{t.Fatal(err)}
	if err:=s.AcceptClassification(validClassification(c,id));err!=nil{t.Fatal(err)}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i:=0;i<8;i++{wg.Add(1);go func(){defer wg.Done();if _,err:=s.BeginResolve(q,id);err==nil{successes.Add(1)}}()}
	wg.Wait()
	if successes.Load()!=1{t.Fatalf("concurrent admission %d",successes.Load())}
}
func TestMetadataBindingRejectsIncompleteIdentity(t *testing.T) {
	_,q,_,id:=sequenceFixture(t)
	cfg:=MetadataConfig{Generation:2,Source:q.Source}
	for _,r:=range []RuntimeIdentity{{},{Generation:1,Version:StrictQualifiedVersion,ArtifactSHA256:id.Runtime.ArtifactSHA256},{Generation:1,Version:"1.32.21",ArtifactSHA256:id.Runtime.ArtifactSHA256,ManifestSHA256:id.Runtime.ManifestSHA256}}{
		if s,err:=NewMetadataSequence(q,r,cfg);s!=nil||err==nil{t.Fatal("unqualified runtime accepted")}
	}
	if s,err:=NewMetadataSequence(q,id.Runtime,MetadataConfig{Generation:2,Source:"e621"});s!=nil||err==nil{t.Fatal("wrong config source")}
	next:=cfg;next.Generation++
	one,_:=cfg.Identity();two,_:=next.Identity()
	if one==two||one.SHA256!=two.SHA256{t.Fatal("generation binding")}
}
