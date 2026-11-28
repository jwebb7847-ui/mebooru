package workerproto

import(
	"strings"
	"testing"
)

func TestStageAllocationExactBoundedContract(t *testing.T){
	q:=StageAllocateRequest{OperationID:strings.Repeat("a",32),Transfer:TransferPlanRequest{
		Metadata:MetadataPlanRequest{JobID:7,Gallery:"A",Kind:MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/search?q=fixture",First:1,Last:3},
		Subcategory:"search",PolicyGeneration:1,ManifestSHA256:strings.Repeat("b",64),
	}}
	request,err:=EncodeStageAllocateRequest(q)
	if err!=nil{t.Fatal(err)}
	got,err:=DecodeStageAllocateRequest(request)
	if err!=nil||got!=q{t.Fatalf("request %+v %v",got,err)}
	for _,bad:=range []string{
		strings.Replace(string(request),q.OperationID,strings.Repeat("0",32),1),
		strings.Replace(string(request),q.OperationID,strings.Repeat("A",32),1),
		strings.Replace(string(request),`"job_id":7`,`"job_id":7,"path":"/tmp"`,1),
		strings.Replace(string(request),`"operation_id":`,`"workspace":"/tmp","operation_id":`,1),
		strings.Replace(string(request),`"operation_id":`,`"max_bytes":1000,"operation_id":`,1),
		string(request)+" {}",
	}{
		if _,err:=DecodeStageAllocateRequest([]byte(bad));err==nil{t.Fatalf("accepted %s",bad)}
	}
	a:=StageAllocation{Request:q,TransferID:strings.Repeat("c",32),MaxFiles:2,MaxBytes:MaxStageTotalBytes}
	body,err:=EncodeStageAllocation(a,q)
	if err!=nil{t.Fatal(err)}
	result,err:=DecodeStageAllocation(body,q)
	if err!=nil||result!=a{t.Fatalf("allocation %+v %v",result,err)}
	for _,mutate:=range []func(*StageAllocateRequest){
		func(r *StageAllocateRequest){r.OperationID=strings.Repeat("d",32)},
		func(r *StageAllocateRequest){r.Transfer.Metadata.Gallery="B"},
		func(r *StageAllocateRequest){r.Transfer.Metadata.JobID++},
		func(r *StageAllocateRequest){r.Transfer.PolicyGeneration++},
		func(r *StageAllocateRequest){r.Transfer.ManifestSHA256=strings.Repeat("d",64)},
	}{
		other:=q;mutate(&other)
		if _,err:=DecodeStageAllocation(body,other);err==nil{t.Fatal("allocation accepted for another operation")}
	}
	for _,mutate:=range []func(*StageAllocation){
		func(r *StageAllocation){r.TransferID=q.OperationID},
		func(r *StageAllocation){r.MaxFiles=4},
		func(r *StageAllocation){r.MaxFiles=0},
		func(r *StageAllocation){r.MaxBytes=0},
		func(r *StageAllocation){r.MaxBytes=MaxStageTotalBytes+1},
	}{
		other:=a;mutate(&other)
		if _,err:=EncodeStageAllocation(other,q);err==nil{t.Fatal("invalid allocation accepted")}
	}
	abandon:=StageAbandonRequest{OperationID:q.OperationID,TransferID:a.TransferID}
	raw,err:=EncodeStageAbandonRequest(abandon)
	if err!=nil{t.Fatal(err)}
	decoded,err:=DecodeStageAbandonRequest(raw)
	if err!=nil||decoded!=abandon{t.Fatalf("abandon %+v %v",decoded,err)}
	if _,err:=DecodeStageAbandonRequest([]byte(strings.Replace(string(raw),`"transfer_id":`,`"cleaned":true,"transfer_id":`,1)));err==nil{t.Fatal("requester cleanup claim accepted")}
	for _,op:=range []Operation{StageAllocate,StageAbandon}{
		rule,ok:=RuleFor(op)
		if !ok||rule.Caller!=Worker||rule.Network!=Local||AllowedDirection(OperationRequester,op)||AllowedDirection(Library,op){t.Fatal("staging direction")}
	}
}
