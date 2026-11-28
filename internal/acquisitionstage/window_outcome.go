package acquisitionstage

import(
	"context"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// ObserveWindowOutcome exposes only read-only queue history evidence.
// Reconcile exact parent and child closures; do not infer completion
// from an image hash or a worker's success report.
func(a *Allocator) ObserveWindowOutcome(ctx context.Context,grant workerproto.StageAllocation,expectedPolicy string)(workerproto.WindowOutcome,error){
	var zero workerproto.WindowOutcome
	v,err:=a.ObserveBatch(ctx,grant)
	if err!=nil||v.PolicySHA256!=expectedPolicy{return zero,ErrStageRecovery}
	out:=workerproto.WindowOutcome{Allocation:grant,PolicySHA256:v.PolicySHA256,PlanSHA256:v.PlanSHA256,Items:make([]workerproto.WindowItemOutcome,0,len(v.Items))}
	for _,item:=range v.Items{
		i:=workerproto.WindowItemOutcome{PostID:item.PostID,State:item.State}
		if item.State=="committed"{
			w:=item.Receipt
			if w==nil{return zero,ErrStageRecovery}
			switch w.Kind{
			case composedReceiptKind,guardedImageReceiptKind:i.Disposition="new"
			case existingHeldReceiptKind:i.Disposition="held"
			case existingRestoreReceiptKind:i.Disposition="restored"
			default:return zero,ErrStageRecovery
			}
			i.ImageID=w.Row.ImageID;i.SHA256=w.Row.SHA256;i.ReceiptSHA256=w.ReceiptSHA256
		}
		out.Items=append(out.Items,i)
	}
	if !workerproto.ValidWindowOutcome(out,grant,expectedPolicy){return zero,ErrStageRecovery}
	return out,nil
}
