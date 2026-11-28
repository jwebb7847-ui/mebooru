package acquisitionplan

import (
	"slices"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

func TestCompileMetadataExactGrant(t *testing.T) {
	q:=workerproto.MetadataPlanRequest{JobID:2,Gallery:"A",Kind:workerproto.MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/search?q=pony",First:4,Last:20}
	want:=[]string{"--config-ignore","-j","--child-filter","False","-o","follow=false","-o","keywords={}","-o","keywords-global={}","-o","metadata-url=meloader_verified_media_url","-o","metadata-path=false","-o","metadata-http=false","-o","metadata-version=false","-o","metadata-extractor=false","--post-range","4-20","--",q.URL}
	args,err:=CompileMetadata(q,q)
	if err!=nil||!slices.Equal(args,want){t.Fatalf("command: %v %v",args,err)}
	for _,mutate:=range []func(*workerproto.MetadataPlanRequest){
		func(p *workerproto.MetadataPlanRequest){p.JobID++},
		func(p *workerproto.MetadataPlanRequest){p.Gallery="B"},
		func(p *workerproto.MetadataPlanRequest){p.URL="https://derpibooru.org/images/1"},
		func(p *workerproto.MetadataPlanRequest){p.Last++},
		func(p *workerproto.MetadataPlanRequest){p.First++},
		func(p *workerproto.MetadataPlanRequest){p.Source="e621";p.URL="https://e621.net/posts/1"},
		func(p *workerproto.MetadataPlanRequest){p.Kind=workerproto.MetadataClassify;p.First=0;p.Last=0},
	}{
		p:=q;mutate(&p)
		if !workerproto.ValidMetadataPlan(p){t.Fatalf("fixture must be independently valid: %+v",p)}
		if a,err:=CompileMetadata(q,p);a!=nil||err==nil{t.Fatal("widened grant accepted")}
	}
	if a,err:=CompileMetadata(workerproto.MetadataPlanRequest{},q);a!=nil||err==nil{t.Fatal("no grant accepted")}
	classify:=q;classify.Kind=workerproto.MetadataClassify;classify.First=0;classify.Last=0
	a,err:=CompileMetadata(classify,classify)
	if err!=nil||!slices.Equal(a,[]string{"--config-ignore","-E","--",q.URL}){t.Fatalf("classification: %v %v",a,err)}
	// No shared mutable argument slice.
	args[0]="--exec"
	if a,err:=CompileMetadata(q,q);err!=nil||!slices.Equal(a,want){t.Fatal("argument mutation persisted")}
	if !StrictSource("derpibooru","search")||!StrictSource("danbooru","tag")||!StrictSource("e621","post")||
		StrictSource("derpibooru","gallery")||StrictSource("directlink","post"){t.Fatal("source policy changed")}
}
