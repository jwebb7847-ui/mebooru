package workerproto

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jwebb7847-ui/mebooru/shared/contract"
)

// Catalog reads are bounded, local-only and carry no filesystem paths
// or credentials. The explicit gallery reference never defaults to
// whichever gallery happens to be active at the time of the request.
const MaxCatalogEntries = 1024
const MaxCatalogNameBytes = 1024

type GalleryCatalog struct {
	Galleries []contract.Gallery `json:"galleries"`
}
type CategoryCatalogRequest struct {
	Gallery string `json:"gallery"`
}
type CategoryCatalog struct {
	Names []string `json:"names"` // custom categories only
}

func validCatalogName(s string)bool{
	if !utf8.ValidString(s)||len(s)>MaxCatalogNameBytes||strings.TrimSpace(s)==""{return false}
	for _,r:=range s{if unicode.IsControl(r){return false}}
	return true
}

func DecodeCategoryCatalogRequest(body []byte)(CategoryCatalogRequest,error){
	var q CategoryCatalogRequest
	if err:=decodeObject(body,&q,"gallery");err!=nil||!validCatalogName(q.Gallery){return CategoryCatalogRequest{},ErrMessage}
	return q,nil
}

func DecodeGalleryCatalog(body []byte)(GalleryCatalog,error){
	var wire struct{Galleries []json.RawMessage `json:"galleries"`}
	if err:=decodeObject(body,&wire,"galleries");err!=nil||wire.Galleries==nil||len(wire.Galleries)>MaxCatalogEntries{return GalleryCatalog{},ErrMessage}
	out:=GalleryCatalog{Galleries:make([]contract.Gallery,0,len(wire.Galleries))}
	seen:=make(map[string]bool,len(wire.Galleries))
	active:=false
	for _,item:=range wire.Galleries{
		var g struct{
			Name *string `json:"name"`
			Images *int `json:"images"`
			Tags *int `json:"tags"`
			Active *bool `json:"active"`
		}
		if err:=decodeObject(item,&g,"name","images","tags","active");err!=nil||
			g.Name==nil||g.Images==nil||g.Tags==nil||g.Active==nil||
			!validCatalogName(*g.Name)||*g.Images<0||*g.Tags<0||seen[*g.Name]||(*g.Active&&active){
			return GalleryCatalog{},ErrMessage
		}
		seen[*g.Name]=true
		active=active||*g.Active
		out.Galleries=append(out.Galleries,contract.Gallery{Name:*g.Name,Images:*g.Images,Tags:*g.Tags,Active:*g.Active})
	}
	return out,nil
}

func DecodeCategoryCatalog(body []byte)(CategoryCatalog,error){
	var out CategoryCatalog
	if err:=decodeObject(body,&out,"names");err!=nil||out.Names==nil||len(out.Names)>MaxCatalogEntries{return CategoryCatalog{},ErrMessage}
	seen:=make(map[string]bool,len(out.Names))
	for _,name:=range out.Names{
		if !validCatalogName(name)||seen[name]{return CategoryCatalog{},ErrMessage}
		seen[name]=true
	}
	return out,nil
}

// Validate the encoded result with the same bounded decoder on both
// endpoints. Never silently truncate a catalog or emit partial success.
func EncodeGalleryCatalog(c GalleryCatalog)(json.RawMessage,error){
	if len(c.Galleries)>MaxCatalogEntries{return nil,ErrMessage}
	body,err:=json.Marshal(c)
	if err!=nil{return nil,err}
	if _,err:=DecodeGalleryCatalog(body);err!=nil{return nil,err}
	return body,nil
}
func EncodeCategoryCatalog(c CategoryCatalog)(json.RawMessage,error){
	if len(c.Names)>MaxCatalogEntries{return nil,ErrMessage}
	body,err:=json.Marshal(c)
	if err!=nil{return nil,err}
	if _,err:=DecodeCategoryCatalog(body);err!=nil{return nil,err}
	return body,nil
}
