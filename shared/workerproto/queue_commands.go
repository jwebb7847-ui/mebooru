package workerproto

import (
	"encoding/json"
	"encoding/hex"
	"strings"
	"net/url"
)

// QueueCommand is the bounded initial private queue/watch command
// family. A typed command is not permission to start an operation.
// No raw command arguments, paths, arbitrary hosts or credentials.
type QueueCommand struct {
	JobID int64 `json:"job_id,omitempty"`
	IntentID string `json:"intent_id,omitempty"`
	WatchID int64 `json:"watch_id,omitempty"`
	URL string `json:"url,omitempty"`
	Gallery string `json:"gallery,omitempty"`
	MaxItems int `json:"max_items,omitempty"`
	IntervalHours int `json:"interval_hours,omitempty"`
	Page int `json:"page,omitempty"`
	Limit int `json:"limit,omitempty"`
	Force bool `json:"force,omitempty"`
}

// ValidQueueIntentID identifies a durable owner submission, not online permission.
func ValidQueueIntentID(id string)bool{
	b,err:=hex.DecodeString(id)
	return err==nil&&len(b)==16&&hex.EncodeToString(b)==id&&id!=strings.Repeat("0",32)
}
func StrictQueueSource(raw string) string {
	u,err:=url.Parse(raw)
	if err!=nil{return ""}
	var source string
	switch u.Host {
	case "derpibooru.org":source="derpibooru"
	case "danbooru.donmai.us":source="danbooru"
	case "e621.net":source="e621"
	default:return ""
	}
	if !ValidMetadataPlan(MetadataPlanRequest{JobID:1,Gallery:"queue",Kind:MetadataStrictResolve,Source:source,URL:raw,First:1,Last:1}){return ""}
	return source
}

func validWatchInterval(n int)bool{
	switch n{case 0,6,12,24,72,168:return true}
	return false
}
func DecodeQueueCommand(op Operation,body []byte)(QueueCommand,error){
	var q QueueCommand
	if len(body)>16<<10{return q,ErrMessage}
	bad:=func()(QueueCommand,error){return QueueCommand{},ErrMessage}
	switch op{
	case QueueList:
		if decodeObject(body,&q,"page","limit")!=nil||q.Page<1||q.Page>1000000||q.Limit<1||q.Limit>100{return bad()}
	case QueueGet,QueueCancel,QueueContinue,QueueContinueAll,QueueRemoveSeries:
		if decodeObject(body,&q,"job_id")!=nil||q.JobID<1{return bad()}
	case QueueRetry:
		if decodeObject(body,&q,"job_id","force")!=nil||q.JobID<1{return bad()}
	case QueueEnqueue:
		if decodeObject(body,&q,"url","gallery","max_items","intent_id","watch_id")!=nil||q.IntentID!=""&&!ValidQueueIntentID(q.IntentID)||q.WatchID<0||q.WatchID>0&&q.IntentID==""||StrictQueueSource(q.URL)==""||!validCatalogName(q.Gallery)||q.MaxItems<1||q.MaxItems>MaxMetadataWindow{return bad()}
	case QueuePause,QueueResume,QueueClear,QueueClearSucceeded,QueueCancelPending,WatchList:
		if len(body)!=0{return bad()}
	case WatchAdd:
		if decodeObject(body,&q,"url","gallery","interval_hours","max_items")!=nil||StrictQueueSource(q.URL)==""||q.Gallery!=""&&!validCatalogName(q.Gallery)||!validWatchInterval(q.IntervalHours)||q.MaxItems<1||q.MaxItems>MaxMetadataWindow{return bad()}
	case WatchSetInterval:
		if decodeObject(body,&q,"watch_id","interval_hours")!=nil||q.WatchID<1||!validWatchInterval(q.IntervalHours){return bad()}
	case WatchSetItems:
		if decodeObject(body,&q,"watch_id","max_items")!=nil||q.WatchID<1||q.MaxItems<1||q.MaxItems>MaxMetadataWindow{return bad()}
	case WatchCheck,WatchRemove:
		if decodeObject(body,&q,"watch_id")!=nil||q.WatchID<1{return bad()}
	default:return bad()
	}
	return q,nil
}

func EncodeQueueCommand(op Operation,q QueueCommand)(json.RawMessage,error){
	switch op{
	case QueuePause,QueueResume,QueueClear,QueueClearSucceeded,QueueCancelPending,WatchList:
		if q!=(QueueCommand{}){return nil,ErrMessage}
		return nil,nil
	}
	body,err:=json.Marshal(q)
	if err!=nil{return nil,ErrMessage}
	got,err:=DecodeQueueCommand(op,body)
	if err!=nil||got!=q{return nil,ErrMessage}
	return body,nil
}
