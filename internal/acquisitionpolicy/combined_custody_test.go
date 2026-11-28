package acquisitionpolicy

import(
	"context"
	"errors"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/processguard"
)

type combinedPrimary struct{done chan struct{};stopped bool;err error}
func(c *combinedPrimary)Done()<-chan struct{}{return c.done}
func(c *combinedPrimary)Stop(context.Context)error{c.stopped=true;return c.err}
type combinedOperation struct{stopped bool;err error}
func(c *combinedOperation)Activate()error{return nil}
func(c *combinedOperation)Stop(context.Context)error{c.stopped=true;return c.err}

func TestCombinedCustodyRequiresBothProofs(t *testing.T){
	ctx:=context.Background()
	for _,failPrimary:=range []bool{true,false}{
		p:=&combinedPrimary{done:make(chan struct{})}
		op:=&combinedOperation{}
		if failPrimary{p.err=ErrCustodyUnconfirmed}else{op.err=ErrCustodyUnconfirmed}
		s:=processguard.NewOperationSet(1)
		if _,err:=s.Start(ctx,func(context.Context)(processguard.OperationChild,error){return op,nil});err!=nil{t.Fatal(err)}
		c,err:=NewCombinedCustody(p,s)
		if err!=nil{t.Fatal(err)}
		if err:=c.Stop(ctx);!errors.Is(err,ErrCustodyUnconfirmed)||!p.stopped||!op.stopped{t.Fatalf("incomplete proof accepted: %v",err)}
		p.err=nil;op.err=nil
		if err:=c.Stop(ctx);err!=nil{t.Fatal(err)}
	}
}
