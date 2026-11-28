package pipeline

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/kwdict"
	"github.com/monbooru/monloader/internal/mapping"
)

type managedEvidenceTransport func(*http.Request)(*http.Response,error)
func(f managedEvidenceTransport)RoundTrip(r *http.Request)(*http.Response,error){return f(r)}

func TestManagedEvidenceNativeRatingAndCategoryParity(t *testing.T){
	ctx:=context.Background()
	gate:=workerproto.NewDrainGate(ctx)
	if err:=gate.Open();err!=nil{t.Fatal(err)}
	mapper,err:=mapping.New(config.NewProvider(config.Default()),"")
	if err!=nil{t.Fatal(err)}
	scope:=func(context.Context,workerproto.MetadataPlanRequest)bool{return true}
	for _,tc:=range []struct{source,url,rating string}{
		{"danbooru","https://danbooru.donmai.us/posts/1",mapping.RatingSensitive},
		{"e621","https://e621.net/posts/1",mapping.RatingGeneral},
	}{
		t.Run(tc.source,func(t *testing.T){
			q:=workerproto.MetadataPlanRequest{JobID:1,Gallery:"A",Kind:workerproto.MetadataStrictResolve,Source:tc.source,URL:tc.url,First:1,Last:1}
			body:=`[[3,"https://example.invalid/1.jpg",{"id":1,"category":"`+tc.source+`","subcategory":"post","rating":"s","tags":["fixture"],"tags_artist":[],"tags_character":[],"tags_copyright":[],"tags_general":["fixture"],"tags_meta":[]}]]`
			makeWindow:=func(raw string)*ManagedEvidenceWindow{
				w,err:=newManagedEvidenceWindow(q,"post",[]byte(raw),gate,gate.CurrentGeneration(),scope,mapper,nil)
				if err!=nil{t.Fatal(err)}
				return w
			}
			w:=makeWindow(body)
			out,err:=w.Run(ctx,q)
			if err!=nil||len(out)!=1||mapper.Map(out[0].Interpreted).Rating!=tc.rating{t.Fatalf("source semantics: %v %v",out,err)}
			out[0].Interpreted["tags"].([]any)[0]="changed"
			if out[0].Item.Meta["tags"].([]any)[0]!="fixture"{t.Fatal("native metadata mutated")}
			if _,err:=w.Run(ctx,q);err==nil{t.Fatal("replayed evidence")}
			for _,bad:=range []string{
				strings.Replace(body,`"rating":"s"`,`"rating":"invalid"`,1),
				strings.Replace(body,`"rating":"s"`,`"rating":7`,1),
				strings.Replace(body,`"rating":"s",`,"",1),
				strings.Replace(body,`"tags_general":["fixture"]`,`"tags_general":["other"]`,1),
				strings.Replace(body,`"tags_meta":[]`,`"tags_meta":null`,1),
			}{
				if rows,err:=makeWindow(bad).Run(ctx,q);rows!=nil||err==nil{t.Fatalf("accepted malformed evidence %s",bad)}
			}
		})
	}
}
func TestManagedEvidenceDerpibooruTaxonomyAndGeneration(t *testing.T){
	ctx:=context.Background()
	gate:=workerproto.NewDrainGate(ctx)
	if err:=gate.Open();err!=nil{t.Fatal(err)}
	mapper,err:=mapping.New(config.NewProvider(config.Default()),"")
	if err!=nil{t.Fatal(err)}
	q:=workerproto.MetadataPlanRequest{JobID:1,Gallery:"A",Kind:workerproto.MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/images/1",First:1,Last:1}
	body:=[]byte(`[[3,"https://example.invalid/1.jpg",{"id":1,"category":"derpibooru","subcategory":"post","tags":["safe","dashie"],"tag_ids":[1,24568]}]]`)
	scope:=func(context.Context,workerproto.MetadataPlanRequest)bool{return true}
	calls:=0
	transport:=managedEvidenceTransport(func(r *http.Request)(*http.Response,error){
		calls++
		var data string
		switch r.URL.Path{
		case "/api/v1/json/search/tags":
			data=`{"tags":[{"id":1,"name":"safe","category":"rating","aliased_tag":null,"aliases":[]},{"id":24568,"name":"dashie","slug":"dashie","category":null,"aliased_tag":"rainbow+dash","aliases":[]}]}`
		case "/api/v1/json/tags/rainbow+dash":
			data=`{"tag":{"id":39229,"name":"rainbow dash","slug":"rainbow+dash","category":"character","aliased_tag":null,"aliases":["dashie"]}}`
		default:t.Fatal("unexpected source request")
		}
		return &http.Response{StatusCode:200,Header:make(http.Header),Body:io.NopCloser(strings.NewReader(data))},nil
	})
	epoch:=gate.CurrentGeneration()
	newWindow:=func(g workerproto.DrainGeneration)*ManagedEvidenceWindow{
		w,err:=newManagedEvidenceWindow(q,"post",body,gate,g,scope,mapper,transport)
		if err!=nil{t.Fatal(err)}
		return w
	}
	out,err:=newWindow(epoch).Run(ctx,q)
	if err!=nil||len(out)!=1||calls!=2{t.Fatalf("source evidence %v %v calls=%d",out,err,calls)}
	if mapper.Map(out[0].Interpreted).Rating!=mapping.RatingGeneral||out[0].Item.Meta["tags"].([]any)[1]!="dashie"{t.Fatal("raw or rating semantics changed")}
	tags:=out[0].Interpreted[kwdict.InterpretedTagsKey].([]kwdict.InterpretedTag)
	if len(tags)!=1||tags[0].Category!="character"||tags[0].Name!="rainbow dash"{t.Fatalf("alias evidence %v",tags)}
	if err:=gate.Drain(ctx);err!=nil{t.Fatal(err)}
	if err:=gate.Open();err!=nil{t.Fatal(err)}
	if out,err:=newWindow(epoch).Run(ctx,q);out!=nil||!errors.Is(err,workerproto.ErrOffline)||calls!=2{t.Fatal("old evidence generation admitted")}
}
