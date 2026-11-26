package pipeline

import(
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monloader/internal/config"
)

// Local fixture only: actual blacklist/taxonomy/admission/coordinator,
// simulated runtime child and files. The separately qualified pinned
// runtime fixture tests actual HTTP media requests, not this child.
type composedTransferChild struct{
	active atomic.Bool
	cleaned atomic.Bool
	data string
	read func(context.Context)([]byte,bool,error)
	stop func(context.Context)error
}
func(c *composedTransferChild)Activate()error{
	if !c.active.CompareAndSwap(false,true){return errors.New("fixture activated twice")}
	return nil
}
func(c *composedTransferChild)ReadChunk(ctx context.Context)([]byte,bool,error){
	if c.read!=nil{return c.read(ctx)}
	return []byte(c.data),true,nil
}
func(c *composedTransferChild)Result(context.Context)(int,[]byte,error){return 0,nil,nil}
func(c *composedTransferChild)Stop(ctx context.Context)error{
	if c.stop!=nil{if err:=c.stop(ctx);err!=nil{return err}}
	c.cleaned.Store(true)
	return nil
}

func TestManagedPreflightComposesContainedTransfer(t *testing.T){
	for _,mode:=range []string{"valid","changed policy","wrong gallery","changed sidecar","all blocked"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			rule:="character:rainbow_dash violence"
			if mode=="all blocked"{rule="character:dashie"}
			p,provider,gate,q,_:=managedPreflightFixture(t,rule)
			r,err:=p.Run(ctx,q)
			if err!=nil{t.Fatal(err)}
			if mode=="all blocked"{
				if r.Approval!=nil||len(r.BlockedIDs)!=2{t.Fatalf("all blocked: %+v",r)}
				return
			}
			if r.Approval==nil||!slices.Equal(r.BlockedIDs,[]string{"41"}){t.Fatalf("preflight: %+v",r)}
			root,err:=filepath.EvalSymlinks(t.TempDir())
			if err!=nil{t.Fatal(err)}
			set:=processguard.NewOperationSet(1)
			var stages []processguard.StrictTransferStage
			var previous *composedTransferChild
			request:=r.Approval.Request()
			if mode=="wrong gallery"{request.Metadata.Gallery="B"}
			files,err:=r.Approval.runTransferCandidate(ctx,request,set,root,
				func(_ context.Context,stage processguard.StrictTransferStage,args []string)(processguard.StreamProbeChild,error){
					if previous!=nil&&!previous.cleaned.Load(){t.Fatal("prior child not cleaned")}
					stages=append(stages,stage)
					child:=&composedTransferChild{}
					switch stage{
					case processguard.TransferVersion:child.data=acquisitionplan.StrictQualifiedVersion
					case processguard.TransferNegative:child.data=`[[2,{}]]`
					case processguard.TransferPositive:
						child.data=`[[2,{}],[3,"x",{}]]`
						if mode=="changed policy"{
							child.stop=func(context.Context)error{return provider.Update(func(c *config.Config)error{c.Tags.Blacklist=[]string{"explicit"};return nil})}
						}
					case processguard.TransferMedia:
						manifest,e:=os.ReadFile(filepath.Join(root,"strict-blacklist.json"))
						if e!=nil{t.Fatal(e)}
						if string(manifest)==""||!slices.Contains(args,"--filter"){t.Fatal("missing filter")}
						if !slices.Contains(args,"fallback=false"){t.Fatal("fallback enabled")}
						name:=filepath.Join(root,"media","42.jpg")
						if e:=os.WriteFile(name,[]byte("fixture media"),0600);e!=nil{t.Fatal(e)}
						sidecar:=`{"id":42,"category":"derpibooru","subcategory":"search","tags":["explicit","dashie"],"tag_ids":[2,24568],"meloader_verified_media_url":"https://example.invalid/42.jpg"}`
						if mode=="changed sidecar"{sidecar=`{"id":41,"category":"derpibooru","subcategory":"search","tags":["safe","dashie","violence"],"tag_ids":[1,24568,3],"meloader_verified_media_url":"https://example.invalid/41.jpg"}`}
						if e:=os.WriteFile(name+".json",[]byte(sidecar),0600);e!=nil{t.Fatal(e)}
						child.data=name+"\n"
					}
					previous=child
					return child,nil
				})
			if mode=="valid"{
				if err!=nil||len(files)!=1||files[0].ID!="42"||len(stages)!=4||!previous.cleaned.Load(){t.Fatalf("composed result %+v %v %v",files,err,stages)}
			}else{
				if err==nil||files!=nil{t.Fatalf("accepted %s",mode)}
				if mode!="changed sidecar"&&slices.Contains(stages,processguard.TransferMedia){t.Fatal("media started despite refusal")}
			}
			if err:=gate.Drain(ctx);err!=nil{t.Fatal(err)}
			if err:=set.SealAndStop(ctx);err!=nil{t.Fatal(err)}
		})
	}
}

func TestManagedTransferDrainWaitsForActualLeaseCleanup(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second)
	defer cancel()
	p,_,gate,q,_:=managedPreflightFixture(t,"violence")
	result,err:=p.Run(ctx,q)
	if err!=nil||result.Approval==nil{t.Fatalf("preflight %v",err)}
	root,err:=filepath.EvalSymlinks(t.TempDir())
	if err!=nil{t.Fatal(err)}
	set:=processguard.NewOperationSet(1)
	entered,stopEntered,release:=make(chan struct{}),make(chan struct{}),make(chan struct{})
	run:=make(chan error,1)
	go func(){
		files,err:=result.Approval.runTransferCandidate(ctx,result.Approval.Request(),set,root,
			func(_ context.Context,stage processguard.StrictTransferStage,_ []string)(processguard.StreamProbeChild,error){
				child:=&composedTransferChild{}
				switch stage{
				case processguard.TransferVersion:child.data=acquisitionplan.StrictQualifiedVersion
				case processguard.TransferNegative:child.data=`[[2,{}]]`
				case processguard.TransferPositive:child.data=`[[2,{}],[3,"x",{}]]`
				case processguard.TransferMedia:
					child.read=func(c context.Context)([]byte,bool,error){close(entered);<-c.Done();return nil,false,c.Err()}
					child.stop=func(c context.Context)error{close(stopEntered);select{case <-release:return nil;case <-c.Done():return c.Err()}}
				}
				return child,nil
			})
		if files!=nil{run<-errors.New("canceled output published");return}
		run<-err
	}()
	wait:=func(ch <-chan struct{}){t.Helper();select{case <-ch:case <-ctx.Done():t.Fatal(ctx.Err())}}
	wait(entered)
	drain:=make(chan error,1)
	go func(){drain<-gate.Drain(ctx)}()
	wait(stopEntered)
	if err:=gate.Open();!errors.Is(err,workerproto.ErrDrainPending){t.Fatalf("opened during cleanup %v",err)}
	select{case err:=<-drain:t.Fatalf("early offline ack %v",err);default:}
	close(release)
	select{case err:=<-run:if err==nil{t.Fatal("canceled transfer succeeded")};case <-ctx.Done():t.Fatal(ctx.Err())}
	select{case err:=<-drain:if err!=nil{t.Fatal(err)};case <-ctx.Done():t.Fatal(ctx.Err())}
	if err:=set.SealAndStop(ctx);err!=nil{t.Fatal(err)}
}
