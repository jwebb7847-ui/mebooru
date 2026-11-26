package workerproto

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestStreamProbeDTOs(t *testing.T){
	q:=StreamCursor{ID:9,Offset:12}
	for _,body:=range []string{`{}`,`{"id":9}`,`{"id":0,"offset":0}`,`{"id":9,"offset":null}`,`{"id":9,"offset":0,"offset":1}`,`{"id":9,"offset":67108865}`,`{"id":9,"offset":0,"argv":[]}`}{
		if _,err:=DecodeStreamCursor([]byte(body));err==nil{t.Fatalf("accepted cursor: %s",body)}
	}
	body,_:=json.Marshal(q)
	if got,err:=DecodeStreamCursor(body);err!=nil||got!=q{t.Fatalf("cursor: %+v %v",got,err)}
	for _,body:=range []string{`{"id":9,"offset":0,"data":"","eof":false}`,`{"id":9,"offset":0,"data":null,"eof":true}`,`{"id":9,"offset":0,"data":"AA=="}`,`{"id":9,"offset":67108864,"data":"AA==","eof":true}`,`{"id":9,"offset":0,"data":"invalid!","eof":false}`}{
		if _,err:=DecodeStreamChunk([]byte(body));err==nil{t.Fatalf("accepted chunk: %s",body)}
	}
	chunk:=StreamChunk{StreamCursor:q,Data:[]byte{0,255,128,10},EOF:true}
	body,_=json.Marshal(chunk)
	got,err:=DecodeStreamChunk(body)
	if err!=nil||got.StreamCursor!=q||!got.EOF||!bytes.Equal(got.Data,chunk.Data){t.Fatalf("chunk: %+v %v",got,err)}
	chunk.Data=make([]byte,StreamChunkLimit+1)
	body,_=json.Marshal(chunk)
	if _,err:=DecodeStreamChunk(body);err==nil{t.Fatal("oversized chunk accepted")}
	for _,body:=range []string{`{"id":9,"offset":0,"stderr":""}`,`{"id":9,"offset":0,"exit_code":-2,"stderr":""}`,`{"id":9,"offset":0,"exit_code":0,"stderr":null}`,`{"id":9,"offset":0,"exit_code":0,"stderr":"","cleanup":true}`}{
		if _,err:=DecodeStreamTerminal([]byte(body));err==nil{t.Fatalf("accepted terminal: %s",body)}
	}
	end:=StreamTerminal{StreamCursor:q,ExitCode:7,Stderr:[]byte("stderr")}
	body,_=json.Marshal(end)
	v,err:=DecodeStreamTerminal(body)
	if err!=nil||v.StreamCursor!=q||v.ExitCode!=7||!bytes.Equal(v.Stderr,end.Stderr){t.Fatalf("terminal: %+v %v",v,err)}
	end.Stderr=make([]byte,StreamStderrLimit+1)
	body,_=json.Marshal(end)
	if _,err:=DecodeStreamTerminal(body);err==nil{t.Fatal("oversized stderr accepted")}
}
