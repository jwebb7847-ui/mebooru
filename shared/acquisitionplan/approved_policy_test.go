package acquisitionplan

import(
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)
func TestApprovedPolicyExactManifestAndAuthorityDigest(t *testing.T){
	manifest:=[]byte(`{"42":{"category":"derpibooru","subcategory":"search","tags":["explicit","dashie"],"meloader_verified_media_url":"https://example.invalid/42.jpg"}}`)
	sum:=sha256.Sum256(manifest)
	q:=workerproto.TransferPlanRequest{Metadata:workerproto.MetadataPlanRequest{JobID:1,Gallery:"A",Kind:workerproto.MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/search?q=fixture",First:1,Last:2},Subcategory:"search",PolicyGeneration:1,ManifestSHA256:hex.EncodeToString(sum[:])}
	r:=ApprovedPolicyRow{PostID:"42",NativeSHA256:strings.Repeat("a",64),MediaURL:"https://example.invalid/42.jpg",
		SourceLabel:"derpibooru",PostURL:"https://derpibooru.org/images/42",Rating:"explicit",Tags:[]string{"character:rainbow_dash","rating:explicit"},
		Taxonomy:[]ApprovedPolicyTag{{Listed:"dashie",Category:"character",Name:"rainbow dash",Aliases:[]string{"dashie"}}}}
	body,digest,err:=EncodeApprovedPolicy(q,[]ApprovedPolicyRow{r})
	if err!=nil{t.Fatal(err)}
	out,err:=DecodeApprovedPolicy(body,q,digest,manifest)
	if err!=nil||len(out.Rows)!=1||out.Rows[0].PostID!="42"{t.Fatalf("projection %+v %v",out,err)}
	for _,mode:=range []string{"wrong ID","wrong URL","extra row","wrong gallery","wrong digest","changed rating","wrong manifest","duplicate key","unknown field","invalid UTF8"}{
		t.Run(mode,func(t *testing.T){
			rows:=[]ApprovedPolicyRow{r}
			actual:=q
			expected:=q
			m:=manifest
			switch mode{
			case "wrong ID":rows[0].PostID="41"
			case "wrong URL":rows[0].MediaURL="https://example.invalid/99.jpg"
			case "extra row":x:=r;x.PostID="43";rows=append(rows,x)
			case "wrong gallery":expected.Metadata.Gallery="B"
			case "changed rating":rows[0].Rating="general"
			case "wrong manifest":m=[]byte(`{}`)
			case "invalid UTF8":rows[0].SourceLabel=string([]byte{0xff})
			}
			b,h,err:=EncodeApprovedPolicy(actual,rows)
			if mode=="changed rating"||mode=="invalid UTF8"{
				if err==nil{t.Fatal("invalid projection encoded")};return
			}
			if err!=nil{t.Fatal(err)}
			switch mode{
			case "wrong digest":h=strings.Repeat("b",64)
			case "duplicate key","unknown field":
				extra:=`"version":1,`;if mode=="unknown field"{extra=`"untrusted":true,`}
				b=[]byte(strings.Replace(string(b),`"version":1,`,extra+`"version":1,`,1))
				s:=sha256.Sum256(b);h=hex.EncodeToString(s[:])
			}
			if out,err:=DecodeApprovedPolicy(b,expected,h,m);err==nil||out.Version!=0{t.Fatalf("accepted %s",mode)}
		})
	}
	if _,_,err:=EncodeApprovedPolicy(q,[]ApprovedPolicyRow{r,r});err==nil{t.Fatal("duplicate ID")}
}
