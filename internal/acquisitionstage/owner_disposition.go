package acquisitionstage

import(
	"context"
	"database/sql"
	"errors"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/monbooru/monbooru/internal/library"
)

// ResolveOwnerDisposition makes the initial library-side decision from
// the actual gallery DB and verified transfer fact. It is not an import
// permission; CommitSealedForOwner revalidates the entire exact target
// under its own acquisition fence. Capture the returned value for the
// logical operation rather than recalculating it after its own landing.
func ResolveOwnerDisposition(ctx context.Context,set *library.Set,gallery string,file acquisitionplan.VerifiedTransferFile,row acquisitionplan.ApprovedPolicyRow)(string,error){
	if set==nil||file.ID==""||file.ID!=row.PostID||file.Size<=0||!validDigest(file.SHA256)||row.SourceLabel==""||row.PostURL==""{return "",ErrStageRefused}
	var decision string
	err:=set.WithAcquisitionFence(ctx,gallery,func(g *library.Gallery,fence func()error)error{
		if fence()!=nil{return ErrStageRefused}
		tx,err:=g.DB.Read.BeginTx(ctx,&sql.TxOptions{ReadOnly:true});if err!=nil{return err};defer tx.Rollback()
		var id,size int64
		var missing bool
		err=tx.QueryRowContext(ctx,"SELECT id,file_size,is_missing FROM images WHERE sha256=?",file.SHA256).Scan(&id,&size,&missing)
		switch{
		case errors.Is(err,sql.ErrNoRows):decision=OwnerDispositionNew;id=0
		case err!=nil:return err
		case id<=0||size!=file.Size:return ErrStageRecovery
		case missing:decision=OwnerDispositionRestore
		default:decision=OwnerDispositionHeld
		}
		// Existing source refresh/URL replacement is a distinct legacy
		// merge operation. Refuse instead of silently losing old claims.
		var n int
		if err:=tx.QueryRowContext(ctx,"SELECT COUNT(*) FROM image_sources WHERE (site=? AND post_id=?) OR url=? OR parent_url=?",row.SourceLabel,row.PostID,row.PostURL,row.PostURL).Scan(&n);err!=nil||n!=0{return ErrStageRefused}
		if id!=0{
			if err:=tx.QueryRowContext(ctx,"SELECT COUNT(*) FROM image_sources WHERE image_id=? AND site=?",id,row.SourceLabel).Scan(&n);err!=nil||n!=0{return ErrStageRefused}
		}
		if tx.Commit()!=nil||fence()!=nil{return ErrStageRecovery}
		return nil
	})
	if err!=nil{return "",err};return decision,nil
}
