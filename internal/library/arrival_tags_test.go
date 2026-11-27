package library

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/gallery"
)

func TestSharedArrivalClientTagOwnership(t *testing.T){
	ctx:=context.Background()
	dir:=t.TempDir();root:=filepath.Join(dir,"gallery")
	if err:=os.Mkdir(root,0700);err!=nil{t.Fatal(err)}
	g,err:=Open(config.Gallery{Name:"A",GalleryPath:root,DBPath:filepath.Join(dir,"gallery.sqlite"),ThumbnailsPath:filepath.Join(dir,"thumbs")})
	if err!=nil{t.Fatal(err)}
	defer g.Close()
	var b bytes.Buffer
	if err:=png.Encode(&b,image.NewRGBA(image.Rect(0,0,16,16)));err!=nil{t.Fatal(err)}
	res,err:=g.Land(ctx,root,"fixture.png",FromReader(bytes.NewReader(b.Bytes())),LandSpec{Origin:"fixture"})
	if err!=nil||res.Image==nil||res.Duplicate{t.Fatal("land",err)}
	id:=res.Image.ID
	source:="derpibooru"
	merge:=func(raw []string){
		t.Helper()
		sum,warn,err:=gallery.MergeSource(g.DB,g.TagSvc,id,source,"42","https://example.invalid/42","","",gallery.PostFile{},raw,gallery.Withheld{})
		if err!=nil||len(warn)!=0||!sum.SourceAdded{t.Fatal("merge",sum,warn,err)}
	}
	merge([]string{"source_only","shared","rating:general"})
	cat,bare,err:=gallery.ResolveCategoryTag(g.DB,"refused")
	if err!=nil{t.Fatal(err)}
	if _,err:=g.TagSvc.RejectTagName(bare,cat);err!=nil{t.Fatal(err)}
	projection:=contract.ProjectJobExtras(contract.PostFields{Source:source,Tags:[]string{"source_only","shared","rating:general"}},contract.Via,"general",contract.JobExtras{Tags:[]string{"Client Tag","shared","refused","rating:explicit"},Via:"job-client"},true,nil)
	_,warnings,rejected,err:=g.ApplyInitialTags(id,projection.ClientTags,projection.Via)
	if err!=nil||len(warnings)!=0||!slices.Equal(rejected,[]string{"refused"}){t.Fatal("apply client",warnings,rejected,err)}
	count:=func(q string,args ...any)int{
		t.Helper();var n int
		if err:=g.DB.Read.QueryRow(q,args...).Scan(&n);err!=nil{t.Fatal(err)}
		return n
	}
	owned:=func(tag,owner string)int{
		return count(`SELECT COUNT(*) FROM image_tag_sources ts JOIN tags t ON t.id=ts.tag_id WHERE ts.image_id=? AND t.name=? AND ts.source=?`,id,tag,owner)
	}
	if owned("client_tag","job-client")!=1||owned("client_tag",source)!=0||owned("shared","job-client")!=1||owned("shared",source)!=1||owned("refused","job-client")!=0{t.Fatal("wrong ownership")}
	// A later source refetch must not retire the unrelated client tag.
	merge([]string{"source_only","rating:general"})
	if owned("client_tag","job-client")!=1||count(`SELECT COUNT(*) FROM image_tags it JOIN tags t ON t.id=it.tag_id WHERE it.image_id=? AND t.name='client_tag' AND it.stale=0`,id)!=1{t.Fatal("client tag retired")}
	if count(`SELECT COUNT(*) FROM image_tags it JOIN tags t ON t.id=it.tag_id JOIN tag_categories c ON c.id=t.category_id WHERE it.image_id=? AND c.name='rating' AND t.name='general'`,id)!=1{t.Fatal("source rating displaced")}
	if _,warnings,rejected,err:=g.ApplyInitialTags(id,[]string{"default_owner"},"");err!=nil||len(warnings)!=0||len(rejected)!=0||owned("default_owner","api")!=1{t.Fatal("default owner",err)}
}
