package acquisitionplan

import(
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

func TestStrictTransferPlanImmutableSubset(t *testing.T){
	q:=workerproto.MetadataPlanRequest{JobID:8,Gallery:"A",Kind:workerproto.MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/search?q=fixture",First:1,Last:2}
	body:=[]byte(`[[3,"https://example.invalid/1.jpg",{"id":1,"category":"derpibooru","subcategory":"search","tags":["safe","fixture"],"tag_ids":[1,2],"description":"omit"}],[3,"https://example.invalid/2.jpg",{"id":2,"category":"derpibooru","subcategory":"search","tags":["safe","violence"],"tag_ids":[1,3]}]]`)
	posts,err:=workerproto.DecodeMetadataEvidence(body,q,"search")
	if err!=nil{t.Fatal(err)}
	r:=RuntimeIdentity{Generation:1,Version:StrictQualifiedVersion,ArtifactSHA256:strings.Repeat("a",64),ManifestSHA256:strings.Repeat("b",64)}
	c:=MetadataConfig{Generation:1,Source:q.Source}
	ci,err:=c.Identity()
	if err!=nil{t.Fatal(err)}
	id:=MetadataIdentity{Runtime:r,Config:ci}
	makePlan:=func()*StrictTransferPlan{
		p,err:=NewStrictTransferPlan(q,"search",posts,[]string{"1"},7,id,c)
		if err!=nil{t.Fatal(err)}
		return p
	}
	for _,bad:=range [][]string{nil,{"1","1"},{"99"},{"1","99"}}{
		if _,err:=NewStrictTransferPlan(q,"search",posts,bad,7,id,c);err==nil{t.Fatalf("accepted subset %v",bad)}
	}
	p:=makePlan()
	copyOfPlan:=*p
	request:=p.Request()
	for _,mutate:=range []func(*workerproto.TransferPlanRequest){
		func(r *workerproto.TransferPlanRequest){r.Metadata.JobID++},
		func(r *workerproto.TransferPlanRequest){r.Metadata.Gallery="B"},
		func(r *workerproto.TransferPlanRequest){r.Metadata.Last++},
		func(r *workerproto.TransferPlanRequest){r.ManifestSHA256=strings.Repeat("c",64)},
		func(r *workerproto.TransferPlanRequest){r.PolicyGeneration++},
	}{
		bad:=request;mutate(&bad)
		if out,err:=p.Consume(bad,id,7);out!=nil||err==nil{t.Fatal("widened transfer grant")}
	}
	for _,change:=range []func(*MetadataIdentity){
		func(i *MetadataIdentity){i.Runtime.Generation++},
		func(i *MetadataIdentity){i.Config.Generation++},
	}{
		fresh:=makePlan();bad:=id;change(&bad)
		if _,err:=fresh.Consume(fresh.Request(),bad,7);err==nil{t.Fatal("changed identity")}
		if _,err:=fresh.Consume(fresh.Request(),id,7);err==nil{t.Fatal("failed grant revived")}
	}
	fresh:=makePlan()
	if _,err:=fresh.Consume(fresh.Request(),id,8);err==nil{t.Fatal("changed policy")}
	// Mutation after construction cannot change the authorized bytes.
	posts[0].Native[0]='!'
	manifest,err:=p.Consume(request,id,7)
	if err!=nil{t.Fatal(err)}
	var rows map[string]map[string]json.RawMessage
	if json.Unmarshal(manifest,&rows)!=nil||len(rows)!=1||rows["1"]==nil||rows["2"]!=nil||rows["1"]["description"]!=nil||
		string(rows["1"][StrictMediaURLKey])!=`"https://example.invalid/1.jpg"`||string(rows["1"]["tags"])!=`["safe","fixture"]`{t.Fatalf("manifest: %s",manifest)}
	digest:=sha256.Sum256(manifest)
	if hex.EncodeToString(digest[:])!=request.ManifestSHA256{t.Fatal("wrong digest")}
	if _,err:=p.Consume(request,id,7);err==nil{t.Fatal("replayed grant")}
	if _,err:=copyOfPlan.Consume(request,id,7);err==nil{t.Fatal("copied grant replayed")}
	if _,err:=new(StrictTransferPlan).Consume(request,id,7);err==nil{t.Fatal("zero-value grant accepted")}
}
