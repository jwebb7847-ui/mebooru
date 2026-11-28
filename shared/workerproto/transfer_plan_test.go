package workerproto

import(
	"strings"
	"testing"
)

func TestStrictTransferPlanContract(t *testing.T){
	q:=TransferPlanRequest{Metadata:MetadataPlanRequest{JobID:7,Gallery:"A",Kind:MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/search?q=fixture",First:1,Last:4},Subcategory:"search",PolicyGeneration:3,ManifestSHA256:strings.Repeat("a",64)}
	body,err:=EncodeTransferPlanRequest(q)
	if err!=nil{t.Fatal(err)}
	got,err:=DecodeTransferPlanRequest(body)
	if err!=nil||got!=q{t.Fatalf("round trip %+v %v",got,err)}
	for _,bad:=range []string{
		strings.Replace(string(body),`"policy_generation":3`,`"policy_generation":0`,1),
		strings.Replace(string(body),`"subcategory":"search"`,`"subcategory":"pool"`,1),
		strings.Replace(string(body),`"gallery":"A"`,`"gallery":"A","argv":[]`,1),
		strings.Replace(string(body),`"policy_generation":3`,`"policy_generation":3,"policy_generation":4`,1),
		strings.Replace(string(body),`"policy_generation":3`,`"policy_generation":3,"path":"/tmp"`,1),
		strings.Replace(string(body),`"policy_generation":3`,`"policy_generation":3,"approved_ids":["1"]`,1),
		strings.Replace(string(body),strings.Repeat("a",64),strings.Repeat("A",64),1),
		string(body)+" {}",
	}{
		if _,err:=DecodeTransferPlanRequest([]byte(bad));err==nil{t.Fatalf("accepted %s",bad)}
	}
	rule,ok:=RuleFor(OperationStrictTransferStart)
	if !ok||rule.Caller!=OperationRequester||rule.Network!=Online||AllowedDirection(Worker,OperationStrictTransferStart)||AllowedDirection(Library,OperationStrictTransferStart){t.Fatal("invalid transfer direction")}
}
