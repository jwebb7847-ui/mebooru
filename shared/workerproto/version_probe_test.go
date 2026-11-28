package workerproto

import (
	"strings"
	"testing"
)

func TestVersionProbeResultStrict(t *testing.T){
	for _,body:=range []string{
		"{}",`{"version":null}`,`{"version":1}`,`{"Version":"1.32.19"}`,
		`{"version":"1.32.19","version":"1.32.20"}`,`{"version":"1.32.19","path":"/tmp/example"}`,
		`{"version":"1.32.19-dev"}`,`{"version":" 1.32.19"}`,`{"version":"1"}`,
		`{"version":"1.2.3.4.5"}`,`{"version":"`+strings.Repeat("1",64)+`.2"}`,
		`{"version":"1.32.19"} {}`,
	}{
		if _,err:=DecodeVersionProbeResult([]byte(body));err==nil{t.Fatalf("accepted invalid version DTO: %s",body)}
	}
	for _,v:=range []string{"1.32.19","1.32.20","2.0","2.0.0.1"}{
		got,err:=DecodeVersionProbeResult([]byte(`{"version":"`+v+`"}`))
		if err!=nil||got.Version!=v{t.Fatalf("version: %+v %v",got,err)}
	}
}
