package acquisitionpolicy

import(
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/windowregistry"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// VerifyRunningQueueWindow independently observes the durable queue
// row before owner registration. It is NOT authorization by itself:
// callers must also match trusted submitted intent, current online
// lease, stable gallery and current policy. First scope is an explicit
// ordinary strict download window without legacy extra options.
func VerifyRunningQueueWindow(ctx context.Context,dir string,q workerproto.MetadataPlanRequest)error{
	if ctx.Err()!=nil||!workerproto.ValidMetadataPlan(q)||q.Kind!=workerproto.MetadataStrictResolve||!filepath.IsAbs(dir)||filepath.Clean(dir)!=dir{return windowregistry.ErrWindow}
	canonical,err:=filepath.EvalSymlinks(dir);if err!=nil||canonical!=dir{return windowregistry.ErrWindow}
	path:=filepath.Join(dir,"queue.sqlite")
	info,err:=os.Lstat(path);if err!=nil||!info.Mode().IsRegular(){return windowregistry.ErrWindow}
	dsn:="file:"+strings.NewReplacer("%","%25","?","%3f","#","%23").Replace(filepath.ToSlash(path))+"?mode=ro&_pragma=busy_timeout(3000)&_pragma=query_only(1)"
	db,err:=sql.Open("sqlite",dsn);if err!=nil{return err};defer db.Close()
	db.SetMaxOpenConns(1)
	var status string
	var raw []byte
	err=db.QueryRowContext(ctx,"SELECT status,data FROM jobs WHERE id=? AND length(data)<=? ",q.JobID,1<<20).Scan(&status,&raw)
	if err!=nil{return errors.Join(windowregistry.ErrWindow,err)}
	actual,err:=os.Lstat(path);if err!=nil||!os.SameFile(info,actual){return windowregistry.ErrWindow}
	var stored struct{
		Job struct{
			ID int64 `json:"id"`
			URL string `json:"url"`
			Status string `json:"status"`
			Gallery string `json:"gallery"`
			Site string `json:"site"`
			MaxItems int `json:"max_items"`
			Kind string `json:"kind"`
			Force bool `json:"force"`
			Root int64 `json:"root"`
			Folder string `json:"folder"`
			PageURL string `json:"page_url"`
			Tags []string `json:"tags"`
			Commentary string `json:"commentary"`
			Source string `json:"source"`
			Via string `json:"via"`
			ImageID int64 `json:"image_id"`
			Backend string `json:"backend"`
			MD5 string `json:"md5"`
			SHA256 string `json:"sha256"`
			ManagedWindow json.RawMessage `json:"managed_window"`
		} `json:"job"`
		Offset int `json:"offset"`
		Auto bool `json:"auto"`
		Priority bool `json:"priority"`
		Background bool `json:"background"`
		Budgeted bool `json:"budgeted"`
		Watch int64 `json:"watch"`
		ForceTo int `json:"force_to"`
		ContribIDs []int64 `json:"contrib_ids"`
		ContribBacklog bool `json:"contrib_backlog"`
	}
	if json.Unmarshal(raw,&stored)!=nil{return windowregistry.ErrWindow}
	j:=stored.Job
	if status!="running"||j.Status!=status||j.ID!=q.JobID||j.URL!=q.URL||j.Gallery!=q.Gallery||j.Site!=q.Source||
		j.MaxItems!=int(q.Last-q.First+1)||stored.Offset<0||uint64(stored.Offset)+1!=uint64(q.First)||
		j.Kind!=""||j.Force||j.Root!=j.ID||j.Folder!=""||j.PageURL!=""||len(j.Tags)!=0||j.Commentary!=""||
		j.Source!=""||j.Via!=""||j.ImageID!=0||j.Backend!=""||j.MD5!=""||j.SHA256!=""||
		len(j.ManagedWindow)!=0||stored.Auto||stored.Priority||stored.Background||stored.Budgeted||stored.Watch!=0||
		stored.ForceTo!=0||len(stored.ContribIDs)!=0||stored.ContribBacklog{return windowregistry.ErrWindow}
	return ctx.Err()
}

// RegisterRunningWindow binds an independently authorized exact intent
// to the actual running durable row. The owner checks its own trusted
// intent before and after observation; the worker cannot mint grants.
// Check must not acquire the manager lifecycle lock during a drain.
func RegisterRunningWindow(ctx context.Context,dir string,registry *windowregistry.Registry,g windowregistry.Grant,check func(context.Context,windowregistry.Grant)bool)error{
	if registry==nil||check==nil||!check(ctx,g){return windowregistry.ErrWindow}
	if err:=VerifyRunningQueueWindow(ctx,dir,g.Request);err!=nil{return err}
	if !check(ctx,g){return windowregistry.ErrWindow}
	return registry.Register(ctx,g)
}
