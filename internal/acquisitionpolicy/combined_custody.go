package acquisitionpolicy

import (
	"context"
	"errors"

	"github.com/jwebb7847-ui/mebooru/shared/processguard"
)

// CombinedCustody is the eventual custodian's terminal proof boundary:
// the main worker and ALL independent operation groups/jobs. The set
// must be owned by this same custodian from before primary activation,
// with no other descendant launch route. This type alone cannot enforce
// that contract and is not yet used by a production entry.
type CombinedCustody struct{
	primary CustodyChild
	operations *processguard.OperationSet
}
func NewCombinedCustody(primary CustodyChild,operations *processguard.OperationSet)(*CombinedCustody,error){
	if primary==nil||operations==nil||primary.Done()==nil{return nil,ErrCustodyUnconfirmed}
	return &CombinedCustody{primary:primary,operations:operations},nil
}
func(c *CombinedCustody)Done()<-chan struct{}{return c.primary.Done()}

// Either proof failing prevents RunCustodian from writing a receipt.
// Both stop paths run independently so one failure cannot skip the other.
// Stop is retryable; underlying owners must serialize concurrent stops.
func(c *CombinedCustody)Stop(ctx context.Context)error{
	results:=make(chan error,2)
	go func(){results<-c.operations.SealAndStop(ctx)}()
	go func(){results<-c.primary.Stop(ctx)}()
	var errs []error
	for i:=0;i<2;i++{
		select{
		case err:=<-results:if err!=nil{errs=append(errs,err)}
		case <-ctx.Done():return errors.Join(append(errs,ctx.Err())...)
		}
	}
	return errors.Join(errs...)
}
