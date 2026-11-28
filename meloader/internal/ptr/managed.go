package ptr

import(
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monloader/internal/config"
)

// NewManagedEngine wraps the actual repository client, including
// account/contribution requests, with the same gate as the queue.
// Local index reads are unaffected. No automatic startup is performed.
func NewManagedEngine(cfg config.PTRConfig,gate *workerproto.DrainGate)(*Engine,error){
	if gate==nil{return nil,errors.New("missing managed PTR gate")}
	e:=NewEngine(cfg)
	t,ok:=e.client.hc.Transport.(*http.Transport)
	if !ok{return nil,errors.New("unavailable PTR transport")}
	t.DisableKeepAlives=true
	e.client.hc.Transport=&managedTransport{gate:gate,base:t}
	e.managedGate=gate
	return e,nil
}
type managedTransport struct{
	gate *workerproto.DrainGate
	base http.RoundTripper
}
type managedBody struct{
	io.ReadCloser
	finish func(error)
	once sync.Once
	err error
}
func(b *managedBody) Close()error{
	b.once.Do(func(){b.err=b.ReadCloser.Close();b.finish(b.err)})
	return b.err
}
func(t *managedTransport) RoundTrip(r *http.Request)(*http.Response,error){
	ctx,finish,err:=t.gate.Admit(r.Context())
	if err!=nil{return nil,err}
	resp,err:=t.base.RoundTrip(r.Clone(ctx))
	if err!=nil{
		var closeErr error
		if resp!=nil&&resp.Body!=nil{closeErr=resp.Body.Close()}
		finish(closeErr)
		return nil,err
	}
	if resp==nil||resp.Body==nil{finish(errors.New("unconfirmed PTR response"));return nil,errors.New("invalid PTR response")}
	resp.Body=&managedBody{ReadCloser:resp.Body,finish:finish}
	return resp,nil
}
