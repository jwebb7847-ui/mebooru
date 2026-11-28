package acquisitionpolicy

import(
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// QueueDemand is read-only scheduling information, never permission to
// run a job. Queued explicit work is distinct from opted-in due watches.
type QueueDemand struct{
	Queued bool
	WatchDue bool
	NextWatch time.Time
}

// ReadQueueDemand reads the existing durable queue without starting a
// worker or creating a database. It does not retry interrupted jobs.
// Unknown/unreadable state returns an error, not a fabricated demand.
func ReadQueueDemand(ctx context.Context,queueDir string,now time.Time,enableWatches bool)(QueueDemand,error){
	var out QueueDemand
	if !filepath.IsAbs(queueDir)||ctx.Err()!=nil{return out,ErrLifecycleClosed}
	path:=filepath.Join(queueDir,"queue.sqlite")
	info,err:=os.Lstat(path)
	if errors.Is(err,os.ErrNotExist){return out,nil}
	if err!=nil{return out,err}
	if !info.Mode().IsRegular(){return out,errors.New("managed queue store not regular")}
	dsn:="file:"+strings.NewReplacer("%","%25","?","%3f","#","%23").Replace(filepath.ToSlash(path))+"?mode=ro&_pragma=busy_timeout(3000)&_pragma=query_only(1)"
	db,err:=sql.Open("sqlite",dsn);if err!=nil{return out,err};defer db.Close()
	db.SetMaxOpenConns(1)
	var exists int
	if err:=db.QueryRowContext(ctx,"SELECT EXISTS(SELECT 1 FROM jobs WHERE status='queued')").Scan(&exists);err!=nil{return out,err}
	out.Queued=exists!=0
	if !enableWatches{return out,nil}
	rows,err:=db.QueryContext(ctx,"SELECT interval_hours,last FROM watches WHERE interval_hours>0");if err!=nil{return QueueDemand{},err};defer rows.Close()
	for rows.Next(){
		var hours int
		var raw string
		if err:=rows.Scan(&hours,&raw);err!=nil{return QueueDemand{},err}
		switch hours{case 6,12,24,72,168:default:return QueueDemand{},errors.New("invalid persisted watch interval")}
		var v struct{CheckedAt time.Time `json:"checked_at"`}
		if len(raw)>64<<10||json.Unmarshal([]byte(raw),&v)!=nil{return QueueDemand{},errors.New("invalid persisted watch state")}
		next:=v.CheckedAt.Add(time.Duration(hours)*time.Hour)
		if !now.Before(next){out.WatchDue=true}
		if out.NextWatch.IsZero()||next.Before(out.NextWatch){out.NextWatch=next}
	}
	if err:=rows.Err();err!=nil{return QueueDemand{},err}
	return out,nil
}

// RunQueueScheduler is owned by the open library application. No
// unattended background service is created. The caller supplies the
// explicit watch opt-in, wake channel and error reporting. Pending
// user-submitted jobs are still held while the owner is offline.
func(m *Manager)RunQueueScheduler(ctx context.Context,dir string,watchesEnabled func()bool,wake <-chan struct{},report func(error)){
	if watchesEnabled==nil||report==nil{return}
	tick:=time.NewTicker(time.Minute);defer tick.Stop()
	for{
		demand,err:=ReadQueueDemand(ctx,dir,time.Now(),watchesEnabled())
		if err==nil{err=m.TickNetworkDemand(ctx,demand.Queued||demand.WatchDue)}
		if err!=nil&&ctx.Err()==nil{report(err)}
		select{case <-ctx.Done():return;case <-m.lifetime.Done():return;case <-tick.C:case _,ok:=<-wake:if !ok{wake=nil}}
	}
}
