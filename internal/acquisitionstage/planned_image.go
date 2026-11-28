package acquisitionstage

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"

	_ "golang.org/x/image/webp"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/monbooru/monbooru/internal/library"
)

// Only formats with the same native DecodeConfig implementation used by
// gallery ingestion. This witnesses header facts and exact file bytes;
// it does not prove that every frame or pixel can be decoded.
type plannedImageFacts struct {
	Format string
	Width int
	Height int
	MD5 string
}
func guardedImageFormat(name string)string{
	switch filepath.Ext(name){
	case ".png":return "png"
	case ".jpg",".jpeg":return "jpeg"
	case ".gif":return "gif"
	case ".webp":return "webp"
	}
	return ""
}
func validPlannedImageFacts(f acquisitionplan.VerifiedTransferFile,m *plannedImageFacts)bool{
	if m==nil{return false}
	hash,err:=hex.DecodeString(m.MD5)
	return err==nil&&len(hash)==md5.Size&&hex.EncodeToString(hash)==m.MD5&&
		m.Format!=""&&m.Format==guardedImageFormat(f.RelativeName)&&m.Width>0&&m.Height>0
}
func inspectPlannedImage(ctx context.Context,root *os.Root,f acquisitionplan.VerifiedTransferFile,links uint64)(plannedImageFacts,error){
	var zero plannedImageFacts
	if guardedImageFormat(f.RelativeName)==""{return zero,ErrStageRefused}
	file,err:=root.Open(f.RelativeName)
	if err!=nil{return zero,ErrStageRecovery}
	cfg,format,decodeErr:=image.DecodeConfig(file)
	closeErr:=file.Close()
	if decodeErr!=nil||closeErr!=nil||format!=guardedImageFormat(f.RelativeName)||cfg.Width<=0||cfg.Height<=0{return zero,ErrStageRefused}
	h:=md5.New()
	if err:=readCheckedLinks(ctx,root,f.RelativeName,f.Size,f.SHA256,h,links);err!=nil{return zero,err}
	return plannedImageFacts{Format:format,Width:cfg.Width,Height:cfg.Height,MD5:hex.EncodeToString(h.Sum(nil))},nil
}
func verifyPlannedImageRow(ctx context.Context,g *library.Gallery,id int64,f acquisitionplan.VerifiedTransferFile,m *plannedImageFacts)error{
	if m==nil{return nil} // historical PNG-only guarded plan
	if !validPlannedImageFacts(f,m){return ErrStageRecovery}
	var n int
	if err:=g.DB.Read.QueryRowContext(ctx,`SELECT COUNT(*) FROM images WHERE id=? AND sha256=? AND file_size=? AND file_type=? AND width=? AND height=? AND md5=?`,id,f.SHA256,f.Size,m.Format,m.Width,m.Height,m.MD5).Scan(&n);err!=nil||n!=1{return ErrStageRecovery}
	return nil
}
