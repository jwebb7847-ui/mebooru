package workerproto

import "testing"

func TestStrictControlDTOs(t *testing.T){
	for _,body:=range []string{`{}`,`{"Offline":false}`,`{"offline":null}`,`{"offline":false,"offline":true}`,`{"offline":false,"extra":true}`}{
		if _,err:=DecodeOfflinePolicy([]byte(body));err==nil{t.Fatalf("accepted policy %s",body)}
	}
	for _,body:=range []string{`{}`,`{"offline":true}`,`{"drained":true}`,`{"offline":true,"drained":null}`}{
		if _,err:=DecodeOfflineAck([]byte(body));err==nil{t.Fatalf("accepted ack %s",body)}
	}
	if got,err:=DecodeOfflineAck([]byte(`{"offline":true,"drained":true}`));err!=nil||!got.Offline||!got.Drained{t.Fatalf("ack: %+v %v",got,err)}
	if got,err:=DecodeIdleReservation([]byte(`{"reserved":false}`));err!=nil||got.Reserved{t.Fatalf("reservation: %+v %v",got,err)}
	if _,err:=DecodeIdleReservation([]byte(`{}`));err==nil{t.Fatal("accepted missing reservation")}
}
