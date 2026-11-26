package workerproto

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxMetadataOutputBytes = 64 << 20
const MaxMetadataPostBytes = 2 << 20
const MaxMetadataTagCount = 8192

// MetadataPost retains exact native metadata, including integer spelling.
// It is not an approved transfer or a verified taxonomy/rating verdict.
type MetadataPost struct {
	ID string
	MediaURL string
	Native json.RawMessage
}

// QualifiedMetadataSource is shared with the strict transfer qualification.
func QualifiedMetadataSource(source,subcategory string) bool {
	switch source {
	case "derpibooru": return subcategory=="post"||subcategory=="search"
	case "danbooru","e621": return subcategory=="post"||subcategory=="tag"
	default: return false
	}
}

func metadataText(raw json.RawMessage)(string,bool){
	var value *string
	if json.Unmarshal(raw,&value)!=nil||value==nil{return "",false}
	return *value,true
}

func metadataID(raw json.RawMessage)(string,bool){
	s:=string(raw)
	if len(s)>0&&s[0]=='"'{
		var ok bool
		s,ok=metadataText(raw)
		if !ok{return "",false}
	}
	id,err:=strconv.ParseUint(s,10,64)
	return s,err==nil&&id>0&&strconv.FormatUint(id,10)==s
}

func metadataURL(s string)bool{
	if len(s)==0||len(s)>MaxMetadataURLBytes||
		strings.ContainsFunc(s,func(r rune)bool{return unicode.IsControl(r)||unicode.IsSpace(r)}){return false}
	u,err:=url.Parse(s)
	return err==nil&&(u.Scheme=="http"||u.Scheme=="https")&&u.Hostname()!=""&&u.User==nil&&u.Fragment==""&&u.Opaque==""
}

func validNativeTags(raw json.RawMessage)bool{
	var names []*string
	if json.Unmarshal(raw,&names)!=nil||len(names)==0||len(names)>MaxMetadataTagCount{return false}
	for _,name:=range names{
		if name==nil||strings.TrimSpace(*name)==""||len(*name)>MaxResolveNameBytes{return false}
	}
	return true
}

// DecodeMetadataEvidence accepts only the pinned strict extractor message
// family: directory metadata and file URL/metadata. No handoffs, unknown
// messages or partial errors. Exact job-window binding is checked by the
// containing operation, not inferred from the output. It preserves raw
// fields for the existing source semantics and blacklist validation;
// it cannot prove completeness or authorize a media request.
func DecodeMetadataEvidence(body []byte,q MetadataPlanRequest,subcategory string)([]MetadataPost,error){
	if !ValidMetadataPlan(q)||q.Kind!=MetadataStrictResolve||
		!QualifiedMetadataSource(q.Source,subcategory)||len(body)>MaxMetadataOutputBytes||!utf8.Valid(body){
		return nil,ErrMessage
	}
	d:=json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if tok,err:=d.Token();err!=nil||tok!=json.Delim('['){return nil,ErrMessage}
	window:=int(q.Last-q.First+1)
	out:=make([]MetadataPost,0)
	seen:=make(map[string]bool)
	messages:=0
	for d.More(){
		messages++
		if messages>2*window+1{return nil,ErrMessage}
		var raw json.RawMessage
		if d.Decode(&raw)!=nil||len(raw)>MaxMetadataPostBytes{return nil,ErrMessage}
		scan:=json.NewDecoder(bytes.NewReader(raw));scan.UseNumber()
		if scanValue(scan,0)!=nil{return nil,ErrMessage}
		var row []json.RawMessage
		if json.Unmarshal(raw,&row)!=nil||len(row)<2{return nil,ErrMessage}
		switch string(row[0]){
		case "2":
			var header map[string]json.RawMessage
			if len(row)!=2||json.Unmarshal(row[1],&header)!=nil||header==nil{return nil,ErrMessage}
		case "3":
			if len(row)!=3||len(out)>=window{return nil,ErrMessage}
			media,ok:=metadataText(row[1])
			if !ok||!metadataURL(media){return nil,ErrMessage}
			var meta map[string]json.RawMessage
			if json.Unmarshal(row[2],&meta)!=nil||meta==nil{return nil,ErrMessage}
			id,ok:=metadataID(meta["id"])
			source,sourceOK:=metadataText(meta["category"])
			sub,subOK:=metadataText(meta["subcategory"])
			if !ok||seen[id]||!sourceOK||source!=q.Source||!subOK||sub!=subcategory||
				!validNativeTags(meta["tags"]){return nil,ErrMessage}
			if _,exists:=meta["_meloader_interpreted_tags"];exists{return nil,ErrMessage}
			if v,exists:=meta["meloader_verified_media_url"];exists{
				claimed,ok:=metadataText(v)
				if !ok||claimed!=media{return nil,ErrMessage}
			}
			seen[id]=true
			out=append(out,MetadataPost{ID:id,MediaURL:media,Native:row[2]})
		default:return nil,ErrMessage
		}
	}
	if tok,err:=d.Token();err!=nil||tok!=json.Delim(']'){return nil,ErrMessage}
	if _,err:=d.Token();err!=io.EOF{return nil,ErrMessage}
	return out,nil
}
