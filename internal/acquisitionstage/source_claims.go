package acquisitionstage

import(
	"context"
	"database/sql"
	"strings"

	"github.com/monbooru/monbooru/internal/gallery"
)

// Verify only the actual supplied source claims: existing library
// behavior intentionally leaves missing/empty source fields unchanged.
// A source's claimed MD5 and size are never the measured media facts.
func verifySourceClaims(ctx context.Context,tx *sql.Tx,id int64,in gallery.SourceInput,md5 string)error{
	return verifySourceClaimsMode(ctx,tx,id,in,md5,false)
}
// Newly created diagnostic rows have no prior source claims to preserve.
// Require even empty fields and absent annotations to match exactly.
func verifySourceClaimsMode(ctx context.Context,tx *sql.Tx,id int64,in gallery.SourceInput,md5 string,exact bool)error{
	var actualURL,actualMD5,parent,original,commentary,translated,ext string
	var w,h int
	var size int64
	err:=tx.QueryRowContext(ctx,`SELECT url,md5,parent_url,original,commentary,commentary_translated,post_width,post_height,post_size,post_ext
		FROM image_sources WHERE image_id=? AND site=? AND post_id=?`,id,in.Source,in.PostID).
		Scan(&actualURL,&actualMD5,&parent,&original,&commentary,&translated,&w,&h,&size,&ext)
	match:=func(want,actual string)bool{return (!exact&&want=="")||want==actual}
	if err!=nil||actualURL!=in.URL||!match(md5,actualMD5)||!match(in.ParentURL,parent)||!match(in.Original,original)||
		!match(in.Commentary,commentary)||!match(in.Translated,translated)||
		((exact||in.PostFile.Width>0)&&w!=in.PostFile.Width)||((exact||in.PostFile.Height>0)&&h!=in.PostFile.Height)||
		((exact||in.PostFile.Size>0)&&size!=in.PostFile.Size)||!match(strings.ToLower(in.PostFile.Ext),ext){return ErrStageRecovery}
	if len(in.Notes)==0&&!exact{return nil}
	rows,err:=tx.QueryContext(ctx,`SELECT x,y,w,h,body FROM image_annotations WHERE image_id=? AND site=? AND post_id=? AND manual=0 ORDER BY id`,id,in.Source,in.PostID)
	if err!=nil{return ErrStageRecovery}
	defer rows.Close()
	i:=0
	for rows.Next(){
		var x,y,w,h int
		var body string
		if i>=len(in.Notes)||rows.Scan(&x,&y,&w,&h,&body)!=nil{return ErrStageRecovery}
		n:=in.Notes[i]
		if x!=n.X||y!=n.Y||w!=n.W||h!=n.H||body!=n.Body{return ErrStageRecovery}
		i++
	}
	if rows.Err()!=nil||i!=len(in.Notes){return ErrStageRecovery}
	return nil
}
