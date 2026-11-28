package queue

import(
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

type managedFixtureProc func(context.Context,*Job)(error,error)
func(p managedFixtureProc) ProcessManaged(ctx context.Context,j *Job)(error,error){return p(ctx,j)}

func TestManagedQueuePersistedOfflineDrain(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second);defer cancel()
	dir:=t.TempDir()
	st,err:=OpenStore(dir);if err!=nil{t.Fatal(err)}
	defer st.Close()
	entered:=make(chan struct{},2)
	canceled:=make(chan struct{})
	release:=make(chan struct{})
	q,err:=NewManaged(ctx,managedFixtureProc(func(c context.Context,j *Job)(error,error){
		entered<-struct{}{}
		if j.URL=="https://example.invalid/first"{
			<-c.Done()
			close(canceled)
			<-release
			return c.Err(),nil
		}
		j.SetItems([]Item{{PostID:"2",Status:ItemSkipped}})
		return nil,nil
	}),1,20)
	if err!=nil{t.Fatal(err)}
	q.UseStore(st)
	q.Start()
	defer func(){q.Close()}()
	first:=q.Enqueue("https://example.invalid/first",Options{Gallery:"A"})
	second:=q.Enqueue("https://example.invalid/second",Options{Gallery:"A"})
	q.Resume();q.Release()
	select{case <-entered:t.Fatal("offline queue ran");case <-time.After(30*time.Millisecond):}
	if idle,err:=q.ReserveManagedIdle(ctx);err!=nil||idle{t.Fatal("queued demand reserved idle",err)}
	h:=q.ManagedControl(nil)
	if _,fault:=h(ctx,workerproto.SetOffline,json.RawMessage(`{"offline":false}`));fault!=nil{t.Fatal(fault)}
	select{case <-entered:case <-ctx.Done():t.Fatal("did not start")}
	done:=make(chan *workerproto.Fault,1)
	go func(){_,fault:=h(ctx,workerproto.SetOffline,json.RawMessage(`{"offline":true}`));done<-fault}()
	select{case <-canceled:case <-ctx.Done():t.Fatal("did not cancel")}
	select{case <-done:t.Fatal("offline acknowledged before actual cleanup");case <-time.After(30*time.Millisecond):}
	close(release)
	select{case fault:=<-done:if fault!=nil{t.Fatal(fault)};case <-ctx.Done():t.Fatal("drain timeout")}
	j,err:=q.Wait(ctx,first);if err!=nil||j.Status!=JobInterrupted{t.Fatal("first status",j,err)}
	j,err=q.Get(second);if err!=nil||j.Status!=JobQueued{t.Fatal("pending lost",j,err)}
	if err:=q.CloseManaged(ctx);err!=nil{t.Fatal(err)}
	q2,err:=NewManaged(ctx,managedFixtureProc(func(context.Context,*Job)(error,error){return nil,nil}),1,20)
	if err!=nil{t.Fatal(err)}
	q2.UseStore(st);q2.Start();defer q2.Close()
	j,err=q2.Get(second);if err!=nil||j.Status!=JobQueued{t.Fatal("durable pending lost",j,err)}
	if err:=q2.SetManagedOffline(ctx,false);err!=nil{t.Fatal(err)}
	if _,err:=q2.Wait(ctx,second);err!=nil{t.Fatal(err)}
	if idle,err:=q2.ReserveManagedIdle(ctx);err!=nil||!idle{t.Fatal("idle",err)}
	if id:=q2.Enqueue("https://example.invalid/late",Options{});id!=0{t.Fatal("accepted after reservation")}
	if err:=q2.SetManagedOffline(ctx,false);err==nil{t.Fatal("reopened retired queue")}
}
