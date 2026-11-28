package workerproto

import (
	"strings"
	"testing"
)

func TestDecodeMetadataEvidence(t *testing.T){
	q:=MetadataPlanRequest{JobID:1,Gallery:"A",Kind:MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/search?q=fixture",First:1,Last:2}
	row:=`[3,"https://example.invalid/1.jpg",{"id":9007199254740993,"category":"derpibooru","subcategory":"search","tags":["safe","fixture"],"tag_ids":[1,2]}]`
	body:=`[[2,{}],`+row+`]`
	posts,err:=DecodeMetadataEvidence([]byte(body),q,"search")
	if err!=nil||len(posts)!=1||posts[0].ID!="9007199254740993"||!strings.Contains(string(posts[0].Native),`"tag_ids":[1,2]`){t.Fatalf("evidence: %+v %v",posts,err)}
	if posts,err:=DecodeMetadataEvidence([]byte("[]"),q,"search");err!=nil||posts==nil||len(posts)!=0{t.Fatal("empty successful listing refused")}
	for _,bad:=range []string{
		`null`,`{}`,body+` []`,`[[2,null]]`,`[[6,"https://example.invalid",{}]]`,`[[4,{}]]`,
		`[`+row+`,[-1,{"error":"bad"}]]`,`[`+row+`,`+row+`]`,
		strings.Replace(body,`9007199254740993`,`1.0`,1),
		strings.Replace(body,`9007199254740993`,`"01"`,1),
		strings.Replace(body,`"category":"derpibooru"`,`"category":"e621"`,1),
		strings.Replace(body,`"subcategory":"search"`,`"subcategory":"post"`,1),
		strings.Replace(body,`"safe","fixture"`,`null`,1),
		strings.Replace(body,`"safe","fixture"`,`" "`,1),
		strings.Replace(body,`"tag_ids":[1,2]`,`"tag_ids":[1,2],"tags":["override"]`,1),
		strings.Replace(body,`"tag_ids":[1,2]`,`"tag_ids":[1,2],"_meloader_interpreted_tags":[]`,1),
		strings.Replace(body,`"tag_ids":[1,2]`,`"tag_ids":[1,2],"meloader_verified_media_url":"https://example.invalid/2.jpg"`,1),
		strings.Replace(body,`https://example.invalid/1.jpg`,`file:///etc/passwd`,1),
		strings.Replace(body,`https://example.invalid/1.jpg`,`https://user:secret@example.invalid/1.jpg`,1),
	}{
		if got,err:=DecodeMetadataEvidence([]byte(bad),q,"search");got!=nil||err==nil{t.Fatalf("accepted %s",bad)}
	}
	q.Last=1
	two:=`[`+row+`,`+strings.Replace(row,"9007199254740993","2",1)+`]`
	if _,err:=DecodeMetadataEvidence([]byte(two),q,"search");err==nil{t.Fatal("window exceeded")}
}
