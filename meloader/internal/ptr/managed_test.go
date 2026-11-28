package ptr

import(
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

type managedFixtureTransport func(*http.Request)(*http.Response,error)
func(f managedFixtureTransport)RoundTrip(r *http.Request)(*http.Response,error){return f(r)}

func TestManagedPTRTransportDrain(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),5*time.Second);defer cancel()
	gate:=workerproto.NewDrainGate(ctx)
	var calls int
	tpt:=&managedTransport{gate:gate,base:managedFixtureTransport(func(r *http.Request)(*http.Response,error){
		calls++
		return &http.Response{StatusCode:200,Body:io.NopCloser(strings.NewReader("fixture")),Header:make(http.Header),Request:r},nil
	})}
	req,err:=http.NewRequestWithContext(ctx,http.MethodGet,"https://example.invalid",nil);if err!=nil{t.Fatal(err)}
	if _,err:=tpt.RoundTrip(req);err==nil||calls!=0{t.Fatal("offline network admitted")}
	if err:=gate.Open();err!=nil{t.Fatal(err)}
	resp,err:=tpt.RoundTrip(req);if err!=nil{t.Fatal(err)}
	done:=make(chan error,1)
	go func(){done<-gate.Drain(ctx)}()
	select{case <-done:t.Fatal("drained open response");case <-time.After(30*time.Millisecond):}
	if err:=resp.Body.Close();err!=nil{t.Fatal(err)}
	select{case err:=<-done:if err!=nil{t.Fatal(err)};case <-ctx.Done():t.Fatal("drain")}
	if _,err:=tpt.RoundTrip(req);err==nil||calls!=1{t.Fatal("offline request admitted")}
}
