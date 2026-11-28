package pipeline

import(
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monloader/internal/outcome"
	"github.com/monbooru/monloader/internal/queue"
)

func TestManagedWindowQueueOutcomePreservesPartialResults(t *testing.T){
	h:=strings.Repeat("a",64)
	items:=[]queue.Item{{PostID:"1",Status:queue.ItemSkipped,Outcome:outcome.SkippedBlacklist},{PostID:"2",Status:queue.ItemPending},{PostID:"3",Status:queue.ItemPending},{PostID:"4",Status:queue.ItemPending}}
	projection:=acquisitionplan.ApprovedPolicySnapshot{Rows:[]acquisitionplan.ApprovedPolicyRow{{PostID:"2"},{PostID:"3"},{PostID:"4"}}}
	files:=[]acquisitionplan.VerifiedTransferFile{{ID:"2",SHA256:h},{ID:"4",SHA256:h}}
	result:=workerproto.WindowOutcome{Items:[]workerproto.WindowItemOutcome{{PostID:"2",State:"committed",Disposition:"held",ImageID:12,SHA256:h,ReceiptSHA256:h},{PostID:"3",State:"not_transferred"},{PostID:"4",State:"unresolved"}}}
	out,err:=mapManagedWindowOutcome(items,projection,files,result)
	if err!=nil||len(out)!=4||out[0].Outcome!=outcome.SkippedBlacklist||out[1].Outcome!=outcome.Duplicate||out[1].ManagedReceiptSHA256!=h||out[2].Outcome!=outcome.Failed||out[3].Outcome!=outcome.Failed{t.Fatal("incorrect outcomes",out,err)}
	result.Items[0].SHA256=strings.Repeat("b",64)
	if _,err:=mapManagedWindowOutcome(items,projection,files,result);err==nil{t.Fatal("mismatched receipt accepted")}
}
