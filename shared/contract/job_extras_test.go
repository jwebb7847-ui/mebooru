package contract

import (
	"reflect"
	"testing"
)

func TestSharedJobExtrasProjection(t *testing.T){
	custom:=func(name string)bool{return name=="custom"}
	fields:=PostFields{Source:"derpibooru",Commentary:"source text",Tags:[]string{"fixture","rating:general"},PostID:"42",URL:"https://example.invalid/42"}
	for _,tc:=range []struct{
		name string
		fields PostFields
		via,rating string
		extras JobExtras
		claimed bool
		wantSource,wantCommentary,wantVia string
		wantTags []string
	}{
		{"source precedence",fields,Via,"general",JobExtras{Tags:[]string{"Rating:explicit","  Client Name  ","client_name","Artist: Some Name","Custom: Another","general: literal","Namespace: foo","*"},Commentary:"client text",Source:"other",Via:"job"},true,"derpibooru","source text","job",[]string{"artist:some_name","client_name","custom:another","literal","namespace:_foo"}},
		{"fallback source",PostFields{Source:"host",Tags:[]string{"fixture"}},Via,"",JobExtras{Source:"custom-source",Commentary:"client text",Tags:[]string{"rating:sensitive"}},false,"custom-source","client text",Via,[]string{"rating:sensitive"}},
		{"claimed source with empty commentary",PostFields{Source:"derpibooru",CommentaryDText:"[b]source[/b]"},Via,"general",JobExtras{Source:"other",Commentary:"fallback"},true,"derpibooru","fallback",Via,nil},
		{"empty",fields,Via,"general",JobExtras{},true,"derpibooru","source text",Via,nil},
		{"all filtered",fields,Via,"general",JobExtras{Tags:[]string{"RATING: explicit","general:rating:sensitive","***"}},true,"derpibooru","source text",Via,nil},
		{"unrated legacy explicit",PostFields{Source:"derpibooru"},"","",JobExtras{Tags:[]string{"rating:general","rating:general"}},true,"derpibooru","","",[]string{"rating:general"}},
	}{
		t.Run(tc.name,func(t *testing.T){
			got:=ProjectJobExtras(tc.fields,tc.via,tc.rating,tc.extras,tc.claimed,custom)
			if got.Fields.Source!=tc.wantSource||got.Fields.Commentary!=tc.wantCommentary||got.Via!=tc.wantVia||!reflect.DeepEqual(got.ClientTags,tc.wantTags){t.Fatalf("projection %+v want tags %v",got,tc.wantTags)}
			want:=tc.fields
			want.Source=tc.wantSource;want.Commentary=tc.wantCommentary
			if !reflect.DeepEqual(got.Fields,want){t.Fatal("source evidence changed")}
		})
	}
}
