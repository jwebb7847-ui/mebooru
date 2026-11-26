package processguard

import(
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type operationFixture struct{
	armed atomic.Bool
	stopped atomic.Bool
	fail atomic.Bool
}
func(c *operationFixture)Activate()error{c.armed.Store(true);return nil}
func(c *operationFixture)Stop(context.Context)error{
	if c.fail.Load(){return ErrOperationUnconfirmed}
	c.stopped.Store(true)
	return nil
}
func startOperationFixture(c *operationFixture)func(context.Context)(OperationChild,error){
	return func(context.Context)(OperationChild,error){return c,nil}
}
func TestOperationSetIndividualStopAndRetry(t *testing.T){
	ctx:=context.Background()
	s:=NewOperationSet(2)
	a,b:=new(operationFixture),new(operationFixture)
	la,err:=s.Start(ctx,startOperationFixture(a))
	if err!=nil{t.Fatal(err)}
	if _,err:=s.Start(ctx,startOperationFixture(b));err!=nil{t.Fatal(err)}
	if _,err:=s.Start(ctx,startOperationFixture(new(operationFixture)));!errors.Is(err,ErrOperationLimit){t.Fatal(err)}
	if err:=la.Stop(ctx);err!=nil{t.Fatal(err)}
	if !a.stopped.Load()||b.stopped.Load(){t.Fatal("individual stop affected sibling")}
	b.fail.Store(true)
	if err:=s.SealAndStop(ctx);!errors.Is(err,ErrOperationUnconfirmed){t.Fatalf("lost failure: %v",err)}
	if _,err:=s.Start(ctx,startOperationFixture(new(operationFixture)));!errors.Is(err,ErrOperationsSealed){t.Fatal(err)}
	b.fail.Store(false)
	if err:=s.SealAndStop(ctx);err!=nil||!b.stopped.Load(){t.Fatalf("retry: %v",err)}
}
func TestOperationSetRetainsStartupFailureAndMissingHandle(t *testing.T){
	ctx:=context.Background()
	s:=NewOperationSet(2)
	c:=new(operationFixture)
	l,err:=s.Start(ctx,func(context.Context)(OperationChild,error){return c,ErrOperationUnconfirmed})
	if l==nil||err==nil||c.armed.Load(){t.Fatal("failed launch not retained dormant")}
	if _,err:=s.Start(ctx,func(context.Context)(OperationChild,error){return nil,nil});!errors.Is(err,ErrOperationUnconfirmed){t.Fatal(err)}
	if err:=s.SealAndStop(ctx);!errors.Is(err,ErrOperationUnconfirmed)||!c.stopped.Load(){t.Fatalf("missing handle accepted: %v",err)}
}
func TestOperationSetSealCannotMissConcurrentLaunch(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),5*time.Second)
	defer cancel()
	s:=NewOperationSet(1)
	c:=new(operationFixture)
	entered,release:=make(chan struct{}),make(chan struct{})
	started,stopped:=make(chan error,1),make(chan error,1)
	go func(){_,err:=s.Start(ctx,func(context.Context)(OperationChild,error){close(entered);<-release;return c,nil});started<-err}()
	<-entered
	go func(){stopped<-s.SealAndStop(ctx)}()
	close(release)
	if err:=<-started;err!=nil{t.Fatal(err)}
	if err:=<-stopped;err!=nil{t.Fatal(err)}
	if !c.armed.Load()||!c.stopped.Load(){t.Fatal("seal missed concurrent start")}
}
