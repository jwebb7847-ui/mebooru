package api

import (
	"net/http"
	"fmt"

	"github.com/jwebb7847-ui/mebooru/shared/contract"
)

func (h *Handler) listGalleries(w http.ResponseWriter, r *http.Request) {
	out,err:=h.galleryCatalog(nil,false)
	if err!=nil{
		contract.WriteError(w,http.StatusInternalServerError,"internal_error",err.Error())
		return
	}
	contract.WriteJSON(w,http.StatusOK,out)
}

// The HTTP compatibility path keeps its existing permissive discovery
// behavior. Managed discovery requires complete results for authorized
// galleries rather than silently hiding a failed gallery.
func (h *Handler) galleryCatalog(allow func(string)bool,strict bool)([]contract.Gallery,error){
	activeName := ""
	if active, ok := h.resolver(""); ok {
		activeName = active.Name
	}
	configured := h.cfg().Galleries
	out := make([]contract.Gallery, 0, len(configured))
	for _, gc := range configured {
		if allow!=nil&&!allow(gc.Name){continue}
		g, ok := h.resolver(gc.Name)
		if !ok||(strict&&(g==nil||g.Closed()||g.Name!=gc.Name)){
			if strict{return nil,fmt.Errorf("gallery unavailable")}
			continue
		}
		images, imagesOK := g.VisibleCount()
		tags, tagsOK := g.TagCount()
		if !imagesOK || !tagsOK {
			return nil,fmt.Errorf("could not count gallery %s",gc.Name)
		}
		out = append(out, contract.Gallery{Name: gc.Name, Images: images, Tags: tags, Active: gc.Name == activeName})
	}
	return out,nil
}
