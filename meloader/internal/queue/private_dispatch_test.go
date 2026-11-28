package queue

import(
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monloader/internal/outcome"
)

func TestPrivateManagedQueuePersistsExactWindow(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second);defer cancel()
	st,err:=OpenStore(t.TempDir());if err!=nil{t.Fatal(err)};defer st.Close()
	q,err:=NewManaged(ctx,managedFixtureProc(func(_ context.Context,j *Job)(error,error){
		s:=j.Snapshot()
		md:=workerproto.MetadataPlanRequest{JobID:s.ID,Gallery:s.Gallery,Kind:workerproto.MetadataStrictResolve,Source:"derpibooru",URL:s.URL,First:1,Last:1}
		g:=workerproto.StageAllocation{Request:workerproto.StageAllocateRequest{OperationID:fmt.Sprintf("%032x",s.ID),Transfer:workerproto.TransferPlanRequest{Metadata:md,Subcategory:"post",PolicyGeneration:1,ManifestSHA256:strings.Repeat("a",64)}},TransferID:strings.Repeat("b",32),MaxFiles:1,MaxBytes:4096}
		hash:=strings.Repeat("c",64)
		if err:=j.BindManagedWindow(g,hash);err!=nil{return err,nil}
		if strings.HasSuffix(s.URL,"/2"){return errors.New("uncertain fixture"),nil}
		row:=workerproto.WindowItemOutcome{PostID:"1",State:"committed",Disposition:"new",ImageID:12,SHA256:strings.Repeat("d",64),ReceiptSHA256:strings.Repeat("e",64)}
		v:=workerproto.WindowOutcome{Allocation:g,PolicySHA256:hash,PlanSHA256:strings.Repeat("f",64),Items:[]workerproto.WindowItemOutcome{row}}
		return j.RecordManagedWindowOutcome(v,[]Item{{PostID:"1",Status:ItemDone,Outcome:outcome.Created,MonbooruID:12,SHA256:row.SHA256,ManagedReceiptSHA256:row.ReceiptSHA256}},true),nil
	}),1,20)
	if err!=nil{t.Fatal(err)}
	q.UseStore(st);q.Start();defer q.Close()
	cr,cw,err:=os.Pipe();if err!=nil{t.Fatal(err)}
	sr,sw,err:=os.Pipe();if err!=nil{cr.Close();cw.Close();t.Fatal(err)}
	defer cr.Close();defer cw.Close();defer sr.Close();defer sw.Close()
	done:=make(chan error,1)
	go func(){
		s,err:=workerproto.OpenSession(ctx,sr,cw,workerproto.Worker,q.PrivateDispatch(func(context.Context,workerproto.Operation,workerproto.QueueCommand)error{return nil},nil))
		if err==nil{<-s.Done();s.Close()}
		done<-err
	}()
	session,err:=workerproto.OpenSession(ctx,cr,sw,workerproto.Library,nil);if err!=nil{t.Fatal(err)}
	defer func(){session.Close();select{case err:=<-done:if err!=nil{t.Error(err)};case <-ctx.Done():t.Error("private session did not close")}}()
	submit:=func(id int)int64{
		body,err:=workerproto.EncodeQueueCommand(workerproto.QueueEnqueue,workerproto.QueueCommand{URL:fmt.Sprintf("https://derpibooru.org/images/%d",id),Gallery:"A",MaxItems:1})
		if err!=nil{t.Fatal(err)}
		e,err:=session.Call(ctx,workerproto.QueueEnqueue,body)
		var v struct{JobID int64 `json:"job_id"`}
		if err!=nil||e.Error!=nil||json.Unmarshal(e.Payload,&v)!=nil||v.JobID<1{t.Fatal("submit",e,err)}
		return v.JobID
	}
	first,second:=submit(1),submit(2)
	stored,err:=st.LoadJobs();if err!=nil||len(stored)!=2||stored[0].Status!=JobQueued||stored[1].Status!=JobQueued{t.Fatal("durable offline submission",err)}
	e,err:=session.Call(ctx,workerproto.QueueEnqueue,json.RawMessage(`{"url":"https://derpibooru.org/images/1","gallery":"A","max_items":1,"path":"/tmp"}`))
	if err!=nil||e.Error==nil{t.Fatal("untyped command",err)}
	e,err=session.Call(ctx,workerproto.SetOffline,json.RawMessage(`{"offline":false}`));if err!=nil||e.Error!=nil{t.Fatal("online",err)}
	j,err:=q.Wait(ctx,first);if err!=nil||j.Status!=JobSucceeded||j.ManagedWindow==nil||j.ManagedWindow.State!="complete"||len(j.Items)!=1||j.Items[0].ManagedReceiptSHA256!=strings.Repeat("e",64){t.Fatal("complete history",j,err)}
	j,err=q.Wait(ctx,second);if err!=nil||j.ManagedWindow==nil||j.ManagedWindow.State!="allocated"{t.Fatal("uncertain history",j,err)}
	body,err:=workerproto.EncodeQueueCommand(workerproto.QueueRetry,workerproto.QueueCommand{JobID:second});if err!=nil{t.Fatal(err)}
	e,err=session.Call(ctx,workerproto.QueueRetry,body);if err!=nil||e.Error==nil{t.Fatal("uncertain window replay",err)}
	e,err=session.Call(ctx,workerproto.SetOffline,json.RawMessage(`{"offline":true}`));if err!=nil||e.Error!=nil{t.Fatal("offline",err)}
	stored,err=st.LoadJobs();if err!=nil||len(stored)!=2||stored[0].ManagedWindow==nil||stored[0].ManagedWindow.State!="complete"||stored[1].ManagedWindow==nil||stored[1].ManagedWindow.State!="allocated"{t.Fatal("durable receipt binding",err)}
}
