package processguard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// Simulate a child which writes excessive partial bytes and then stops
// producing stdout. The monitor must cancel it and prove its cleanup.
func TestAllocatedStrictTransferCancelsOverBudget(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),5*time.Second)
	defer cancel()
	q,c,manifest,root:=transferOperationFixture(t)
	grant:=workerproto.StageAllocation{Request:workerproto.StageAllocateRequest{OperationID:strings.Repeat("a",32),Transfer:q},TransferID:strings.Repeat("b",32),MaxFiles:1,MaxBytes:128}
	set:=NewOperationSet(1)
	gate:=workerproto.NewDrainGate(ctx)
	if err:=gate.Open();err!=nil{t.Fatal(err)}
	epoch:=gate.CurrentGeneration()
	var child *metadataChildFixture
	out,err:=RunAllocatedStrictTransfer(ctx,set,grant,c,root,func(c context.Context,_ workerproto.TransferPlanRequest)(context.Context,[]byte,func(error),error){
		lease,finish,err:=gate.AdmitBound(c,epoch)
		return lease,manifest,finish,err
	},func(context.Context)bool{return true},func(_ context.Context,stage StrictTransferStage,_ []string)(StreamProbeChild,error){
		f:=&metadataChildFixture{}
		switch stage{
		case TransferVersion:f.data=acquisitionplan.StrictQualifiedVersion
		case TransferNegative:f.data=`[[2,{}]]`
		case TransferPositive:f.data=`[[2,{}],[3,"x",{}]]`
		case TransferMedia:
			child=f
			f.read=func(c context.Context)([]byte,bool,error){
				if err:=os.WriteFile(filepath.Join(root,"media","1.jpg.part"),make([]byte,129),0600);err!=nil{return nil,false,err}
				<-c.Done()
				return nil,false,c.Err()
			}
		}
		return f,nil
	},acquisitionplan.StrictQualifiedVersion)
	if err==nil||out!=nil||ctx.Err()!=nil||child==nil||!child.stopped.Load(){t.Fatal("over-budget child not cancelled and cleaned",err)}
	if err:=gate.Drain(ctx);err!=nil{t.Fatal(err)}
	if err:=set.SealAndStop(ctx);err!=nil{t.Fatal(err)}
	if b,err:=os.ReadFile(filepath.Join(root,"media","1.jpg.part"));err!=nil||len(b)!=129{t.Fatal("uncertain partial evidence deleted",err)}
}
