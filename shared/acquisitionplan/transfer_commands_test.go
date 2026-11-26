package acquisitionplan

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

func TestPrepareTransferCommands(t *testing.T){
	manifest:=[]byte(`{"42":{"category":"derpibooru","subcategory":"search","tags":["fixture"],"meloader_verified_media_url":"https://example.invalid/42.jpg"}}`)
	sum:=sha256.Sum256(manifest)
	q:=workerproto.TransferPlanRequest{Metadata:workerproto.MetadataPlanRequest{JobID:1,Gallery:"A",Kind:workerproto.MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/search?q=fixture",First:1,Last:4},Subcategory:"search",PolicyGeneration:1,ManifestSHA256:hex.EncodeToString(sum[:])}
	config:=MetadataConfig{Generation:1,Source:"derpibooru"}
	root,err:=filepath.EvalSymlinks(t.TempDir())
	if err!=nil{t.Fatal(err)}
	c,err:=PrepareTransferCommands(q,manifest,config,root)
	if err!=nil||!c.VerifyFiles(){t.Fatalf("commands %v",err)}
	if !slices.Equal(c.Transfer[len(c.Transfer)-4:],[]string{"--post-range","1-4","--",q.Metadata.URL})||!slices.Contains(c.Transfer,"fallback=false")||!slices.Contains(c.Transfer,"archive="){t.Fatalf("args %v",c.Transfer)}
	if _,err:=PrepareTransferCommands(q,manifest,config,root);err==nil{t.Fatal("reused directory")}
	if err:=os.WriteFile(c.manifestPath,[]byte("{}"),0600);err!=nil{t.Fatal(err)}
	if c.VerifyFiles(){t.Fatal("changed manifest accepted")}
	root,err=filepath.EvalSymlinks(t.TempDir())
	if err!=nil{t.Fatal(err)}
	if _,err:=PrepareTransferCommands(q,[]byte("{}"),config,root);err==nil{t.Fatal("digest mismatch")}
	if !ValidatePredicateProbe([]byte(`[[2,{}]]`),false)||!ValidatePredicateProbe([]byte(`[[2,{}],[3,"https://example.invalid",{}]]`),true)||ValidatePredicateProbe([]byte(`[[2,{}]]`),true)||ValidatePredicateProbe([]byte(`[[2,{}],[3,"https://example.invalid",{}]]`),false){t.Fatal("predicate probe validation")}
}
