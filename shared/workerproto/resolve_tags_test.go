package workerproto

import (
	"strings"
	"testing"
)

func TestResolveTagsTypedContract(t *testing.T) {
	q:=ResolveTagsRequest{Gallery:"A",Names:[]string{"dashie","dashie","","new"}}
	body,err:=EncodeResolveTagsRequest(q)
	if err!=nil{t.Fatal(err)}
	if _,err:=DecodeResolveTagsRequest(body);err!=nil{t.Fatal(err)}
	out:=ResolveTagsResponse{Gallery:"A",Results:[]TagResolution{
		{Name:"dashie",Status:"alias",ResolvesTo:"character:rainbow_dash",Implies:[]string{"pony"}},
		{Name:"dashie",Status:"alias",ResolvesTo:"character:rainbow_dash",Implies:[]string{"pony"}},
		{Name:"",Status:"refused",Reason:"invalid tag name",Implies:[]string{}},
		{Name:"new",Status:"new",Implies:[]string{}},
	}}
	body,err=EncodeResolveTagsResponse(out,q)
	if err!=nil{t.Fatal(err)}
	if got,err:=DecodeResolveTagsResponse(body,q);err!=nil||len(got.Results)!=4{t.Fatalf("valid response: %+v %v",got,err)}
	for _,bad:=range []string{
		`{}`,`{"gallery":"","names":["x"]}`,`{"gallery":"A","names":[]}`,`{"gallery":"A","names":[null]}`,
		`{"gallery":"A","names":["x"],"job_id":1}`,`{"gallery":"A","gallery":"B","names":["x"]}`,
		`{"gallery":"A","Names":["x"]}`,`{"gallery":"A","names":["x"],"path":"secret"}`,
	}{
		if _,err:=DecodeResolveTagsRequest([]byte(bad));err==nil{t.Fatalf("accepted request %s",bad)}
	}
	for _,bad:=range []string{
		`{"gallery":"B","results":[{"name":"x","status":"new","implies":[]}]}`,
		`{"gallery":"A","results":[]}`,
		`{"gallery":"A","results":[{"name":"y","status":"new","implies":[]}]}`,
		`{"gallery":"A","results":[{"status":"new","implies":[]}]}`,
		`{"gallery":"A","results":[{"name":"x","status":"alias","implies":[]}]}`,
		`{"gallery":"A","results":[{"name":"x","status":"new","implies":["y"]}]}`,
		`{"gallery":"A","results":[{"name":"x","status":"exists","implies":[null]}]}`,
		`{"gallery":"A","results":[{"name":"x","status":"refused","implies":[]}]}`,
		`{"gallery":"A","results":[{"name":"x","status":"new","implies":[],"path":"secret"}]}`,
		`{"gallery":"A","results":[{"name":"x","status":"new","implies":[],"status":"exists"}]}`,
	}{
		if _,err:=DecodeResolveTagsResponse([]byte(bad),ResolveTagsRequest{Gallery:"A",Names:[]string{"x"}});err==nil{t.Fatalf("accepted response %s",bad)}
	}
	if _,err:=EncodeResolveTagsRequest(ResolveTagsRequest{Gallery:"A",Names:[]string{strings.Repeat("x",MaxResolveNameBytes+1)}});err==nil{t.Fatal("oversize input accepted")}
	if _,err:=EncodeResolveTagsRequest(ResolveTagsRequest{Gallery:"A",Names:make([]string,MaxResolveNames+1)});err==nil{t.Fatal("oversize batch accepted")}
	out.Results[0].Implies=make([]string,MaxResolveImplications+1)
	if _,err:=EncodeResolveTagsResponse(out,q);err==nil{t.Fatal("oversize implication count accepted")}
	out.Results[0].Implies=[]string{"pony"}
	out.Results[0],out.Results[3]=out.Results[3],out.Results[0]
	if _,err:=EncodeResolveTagsResponse(out,q);err==nil{t.Fatal("reordered results accepted")}
}
