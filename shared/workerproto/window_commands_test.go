package workerproto

import(
	"encoding/json"
	"testing"
)
func TestWindowNoTransferExactOrderedBlockedListing(t *testing.T){
	q:=MetadataPlanRequest{JobID:1,Gallery:"A",Kind:MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/search?q=fixture",First:1,Last:3}
	v:=WindowNoTransferProposal{Request:q,ListedIDs:[]string{"1","2","3"},BlockedIDs:[]string{"1","2","3"}}
	raw,err:=EncodeWindowNoTransfer(v);if err!=nil{t.Fatal(err)}
	got,err:=DecodeWindowNoTransfer(raw);if err!=nil||got.Request!=q||len(got.ListedIDs)!=3{t.Fatal(got,err)}
	for _,bad:=range []WindowNoTransferProposal{
		{Request:q,ListedIDs:[]string{"1","2"},BlockedIDs:[]string{"2","1"}},
		{Request:q,ListedIDs:[]string{"1","1"},BlockedIDs:[]string{"1","1"}},
		{Request:q,ListedIDs:[]string{"0"},BlockedIDs:[]string{"0"}},
		{Request:q,ListedIDs:[]string{"01"},BlockedIDs:[]string{"01"}},
		{Request:q,ListedIDs:[]string{"1","2"},BlockedIDs:[]string{"1"}},
		{Request:q,ListedIDs:[]string{"1","2","3","4"},BlockedIDs:[]string{"1","2","3","4"}},
		{Request:q,ListedIDs:nil,BlockedIDs:nil},
	}{
		if _,err:=EncodeWindowNoTransfer(bad);err==nil{t.Fatal("invalid no-transfer encoded",bad)}
	}
	extra:=append(append([]byte{},raw[:len(raw)-1]...),[]byte(`,"approved":true}`)...)
	if _,err:=DecodeWindowNoTransfer(extra);err==nil{t.Fatal("unexpected field")}
	empty:=WindowNoTransferProposal{Request:q,ListedIDs:[]string{},BlockedIDs:[]string{}}
	body,err:=json.Marshal(empty);if err!=nil{t.Fatal(err)}
	if _,err:=DecodeWindowNoTransfer(body);err!=nil{t.Fatal("exact empty listing",err)}
}


func TestWindowPrepareRequiresCanonicalIntent(t *testing.T){
	q:=MetadataPlanRequest{JobID:1,Gallery:"A",Kind:MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/images/1",First:1,Last:1}
	v:=WindowPrepareRequest{Request:q,IntentID:"0123456789abcdef0123456789abcdef"}
	raw,err:=EncodeWindowPrepare(v);if err!=nil{t.Fatal(err)}
	got,err:=DecodeWindowPrepare(raw);if err!=nil||got!=v{t.Fatal(got,err)}
	for _,id:=range []string{"","0","00000000000000000000000000000000","0123456789ABCDEF0123456789ABCDEF","../not-an-intent"}{
		bad:=v;bad.IntentID=id
		if _,err:=EncodeWindowPrepare(bad);err==nil{t.Fatal("invalid intent",id)}
	}
}
