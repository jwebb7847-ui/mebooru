package acquisitionpolicy

import(
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestDurableQueueDemandAndOfflineScheduler(t *testing.T){
	ctx:=context.Background()
	var log []string
	starts:=0
	m,_,dir:=newTestManager(t,func(_, _ context.Context,f *WorkerFence)(ManagedPeer,error){
		if err:=f.Arm();err!=nil{return nil,err};starts++
		return &lifecyclePeer{fakePeer:fakePeer{log:&log}},nil
	})
	now:=time.Now()
	d,err:=ReadQueueDemand(ctx,dir,now,true);if err!=nil||d.Queued||d.WatchDue{t.Fatal(d,err)}
	db,err:=sql.Open("sqlite",filepath.Join(dir,"queue.sqlite"));if err!=nil{t.Fatal(err)}
	defer db.Close()
	if _,err:=db.Exec(`CREATE TABLE jobs(status TEXT);CREATE TABLE watches(interval_hours INT,last TEXT);INSERT INTO jobs VALUES('queued');INSERT INTO watches VALUES(6,'{"checked_at":"0001-01-01T00:00:00Z"}')`);err!=nil{t.Fatal(err)}
	d,err=ReadQueueDemand(ctx,dir,now,false);if err!=nil||!d.Queued||d.WatchDue{t.Fatal(d,err)}
	if err:=m.TickNetworkDemand(ctx,d.Queued);err!=nil||starts!=0{t.Fatal("offline scheduler woke worker",err)}
	if err:=m.SetOffline(ctx,false);err!=nil{t.Fatal(err)}
	if err:=m.TickNetworkDemand(ctx,d.Queued);err!=nil||starts!=1{t.Fatal("online explicit demand failed",err)}
	if _,err:=db.Exec("UPDATE jobs SET status='interrupted'");err!=nil{t.Fatal(err)}
	d,err=ReadQueueDemand(ctx,dir,now,false);if err!=nil||d.Queued||d.WatchDue{t.Fatal("interrupted job replay or opt-in bypass",d,err)}
	d,err=ReadQueueDemand(ctx,dir,now,true);if err!=nil||!d.WatchDue{t.Fatal("due opted-in watch lost",d,err)}
}
