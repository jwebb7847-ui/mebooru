package workerproto

import (
	"strings"
	"testing"
)

func TestMetadataPlanContract(t *testing.T) {
	q := MetadataPlanRequest{JobID:42,Gallery:"A",Kind:MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/search?q=rainbow+dash",First:1,Last:401}
	body,err:=EncodeMetadataPlanRequest(q)
	if err!=nil{t.Fatal(err)}
	if got,err:=DecodeMetadataPlanRequest(body);err!=nil||got!=q{t.Fatalf("round trip: %+v %v",got,err)}
	for _,bad:=range []string{
		`{}`,
		`{"job_id":1,"gallery":"A","kind":"classify","source":"derpibooru","url":"https://derpibooru.org/images/1","first":0}`,
		`{"job_id":1,"gallery":"A","kind":"strict_resolve","source":"derpibooru","url":"https://derpibooru.org/images/1","first":1}`,
		`{"job_id":1,"gallery":"A","kind":"classify","source":"derpibooru","url":"https://derpibooru.org/images/1","argv":["--exec","x"]}`,
		`{"job_id":1,"gallery":"A","kind":"classify","source":"derpibooru","url":"https://derpibooru.org/images/1","config":"/secret"}`,
		`{"job_id":1,"gallery":"A","kind":"classify","source":"derpibooru","url":"https://derpibooru.org/images/1","url":"https://other.invalid/"}`,
		`{"job_id":1,"gallery":"A","kind":"classify","source":"derpibooru","url":"https://derpibooru.org/images/1","first":null}`,
	}{
		if _,err:=DecodeMetadataPlanRequest([]byte(bad));err==nil{t.Fatalf("accepted %s",bad)}
	}
	for _,u:=range []string{
		"http://derpibooru.org/images/1","https://derpibooru.org.evil.invalid/","https://derpibooru.org@evil.invalid/",
		"https://user:secret@derpibooru.org/images/1","https://derpibooru.org:443/images/1","file:///etc/passwd",
		"--exec=x","https://127.0.0.1/images/1","https://derpibooru.org/images/1#x",
		"https://derpibooru.org/images/1\n","https://derpibooru.org/\\x","https://derpibooru.org/"+strings.Repeat("a",MaxMetadataURLBytes),
	}{
		bad:=q;bad.URL=u
		if ValidMetadataPlan(bad){t.Fatalf("accepted URL %q",u)}
	}
	for _,window:=range [][2]uint32{{0,1},{2,1},{1,1002},{1,0},{2147483647,2147483648}}{
		bad:=q;bad.First,bad.Last=window[0],window[1]
		if ValidMetadataPlan(bad){t.Fatalf("accepted range %v",window)}
	}
	bad:=q;bad.JobID=0
	if ValidMetadataPlan(bad){t.Fatal("missing job accepted")}
	bad=q;bad.Gallery=""
	if ValidMetadataPlan(bad){t.Fatal("missing gallery accepted")}
	bad=q;bad.Source="e621"
	if ValidMetadataPlan(bad){t.Fatal("mismatched source accepted")}
	for _,source:=range []struct{name,url string}{{"danbooru","https://danbooru.donmai.us/posts/1"},{"e621","https://e621.net/posts/1"}}{
		x:=MetadataPlanRequest{JobID:1,Gallery:"B",Kind:MetadataClassify,Source:source.name,URL:source.url}
		b,err:=EncodeMetadataPlanRequest(x)
		if err!=nil{t.Fatal(err)}
		if got,err:=DecodeMetadataPlanRequest(b);err!=nil||got!=x{t.Fatalf("classification: %+v %v",got,err)}
	}
	rule,ok:=RuleFor(OperationMetadataStart)
	if !ok||rule.Caller!=OperationRequester||rule.Network!=Online||
		AllowedDirection(Worker,OperationMetadataStart)||AllowedDirection(Library,OperationMetadataStart){
		t.Fatalf("wrong metadata direction: %+v",rule)
	}
}
