package workerproto

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/contract"
)

func TestCatalogStrictValidation(t *testing.T){
	for _,body:=range []string{`{}`,`{"galleries":null}`,`{"galleries":[{"name":"a","images":0,"tags":0}]}`,`{"galleries":[{"name":"a","images":-1,"tags":0,"active":false}]}`,`{"galleries":[{"name":"a","images":0,"tags":0,"active":false,"path":"/secret"}]}`,`{"galleries":[{"name":"a","images":0,"tags":0,"active":false},{"name":"a","images":0,"tags":0,"active":false}]}`,`{"galleries":[{"name":"a","images":0,"tags":0,"active":true},{"name":"b","images":0,"tags":0,"active":true}]}`}{
		if _,err:=DecodeGalleryCatalog([]byte(body));err==nil{t.Fatalf("accepted gallery catalog: %s",body)}
	}
	for _,body:=range []string{`{}`,`{"names":null}`,`{"names":[null]}`,`{"names":["x","x"]}`,`{"names":[" "]}`,`{"names":["x"],"path":"secret"}`}{
		if _,err:=DecodeCategoryCatalog([]byte(body));err==nil{t.Fatalf("accepted category catalog: %s",body)}
	}
	for _,body:=range []string{`{}`,`{"gallery":""}`,`{"gallery":null}`,`{"Gallery":"a"}`,`{"gallery":"a","gallery":"b"}`,`{"gallery":"a","path":"secret"}`}{
		if _,err:=DecodeCategoryCatalogRequest([]byte(body));err==nil{t.Fatalf("accepted request: %s",body)}
	}
	q,_:=json.Marshal(CategoryCatalogRequest{Gallery:"Gallery A"})
	if got,err:=DecodeCategoryCatalogRequest(q);err!=nil||got.Gallery!="Gallery A"{t.Fatalf("valid request: %+v %v",got,err)}
	if _,err:=EncodeGalleryCatalog(GalleryCatalog{Galleries:[]contract.Gallery{{Name:"Gallery A",Images:1,Tags:2,Active:true}}});err!=nil{t.Fatal(err)}
	if _,err:=EncodeCategoryCatalog(CategoryCatalog{Names:[]string{}});err!=nil{t.Fatal(err)}
	if _,err:=EncodeCategoryCatalog(CategoryCatalog{Names:[]string{strings.Repeat("x",MaxCatalogNameBytes+1)}});err==nil{t.Fatal("oversize accepted")}
}
