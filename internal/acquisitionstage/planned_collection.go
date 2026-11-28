package acquisitionstage

import (
	"context"
	"database/sql"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/library"
)

// Explicit ordered source collection for a genuinely new image. Bind
// the actual legacy label resolution before publication. Other members
// of the collection are not owned by this operation.
type plannedCollection struct {
	Name string
	Order int
	Label string
}

func validPlannedCollection(c *plannedCollection)bool{
	return c!=nil&&validCollectionLabel(c.Name)&&validCollectionLabel(c.Label)&&
		strings.EqualFold(c.Name,c.Label)&&c.Order>0
}
func matchesCollectionClaim(c *plannedCollection,p *acquisitionplan.ApprovedProvenance)bool{
	if p==nil||!validCollectionClaim(p){return false}
	if p.Collection==""{return c==nil}
	return validPlannedCollection(c)&&c.Name==p.Collection&&c.Order==p.CollectionOrder
}
func inspectPlannedCollection(ctx context.Context,g *library.Gallery,p *acquisitionplan.ApprovedProvenance)(*plannedCollection,error){
	if p==nil||!validCollectionClaim(p){return nil,ErrStageRefused}
	if p.Collection==""{return nil,nil}
	tx,err:=g.DB.Read.BeginTx(ctx,&sql.TxOptions{ReadOnly:true})
	if err!=nil{return nil,ErrStageRecovery}
	defer tx.Rollback()
	label,err:=gallery.ExistingLabelTx(tx,p.Collection)
	if err!=nil{return nil,ErrStageRecovery}
	c:=&plannedCollection{Name:p.Collection,Order:p.CollectionOrder,Label:label}
	if !validPlannedCollection(c){return nil,ErrStageRefused}
	if err:=tx.Commit();err!=nil||ctx.Err()!=nil{return nil,ErrStageRecovery}
	return c,nil
}

func matchesPlannedCollectionAttempt(c *plannedCollection,m collectionAttempt)bool{
	return validPlannedCollection(c)&&m.NewOnly&&m.Name==c.Name&&m.Order==c.Order&&
		m.Before.Label==c.Label&&m.Before.Member==c.Label&&m.Before.State.Home==""&&
		m.Before.State.Position==nil&&len(m.Before.State.Members)==0
}
