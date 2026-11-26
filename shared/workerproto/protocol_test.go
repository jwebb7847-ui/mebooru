package workerproto

import (
	"encoding/json"
	"testing"
)

func TestOperationDirections(t *testing.T) {
	for op, rule := range rules {
		if rule.Caller != Library && rule.Caller != Worker && rule.Caller != OperationRequester {
			t.Fatalf("%s: invalid caller %q", op, rule.Caller)
		}
		other := counterpart(rule.Caller)
		if !AllowedDirection(rule.Caller, op) || AllowedDirection(other, op) ||
			AllowedDirection(Role("external"), op) {
			t.Fatalf("%s: invalid direction classification", op)
		}
		if rule.Network != Local && rule.Network != Online && rule.Network != Mixed {
			t.Fatalf("%s: invalid network classification", op)
		}
		if rule.Caller == Worker && rule.Network != Local {
			t.Fatalf("%s: library callback must be local", op)
		}
	}
	for _, op := range []Operation{"", "library.delete_media", "library.sql", "http.proxy", "file.read", "exec", "credential.export"} {
		if _, ok := RuleFor(op); ok || AllowedDirection(Library, op) || AllowedDirection(Worker, op) {
			t.Fatalf("unexpected operation %q", op)
		}
	}
}

func TestNetworkPolicyExamples(t *testing.T) {
	for _, tc := range []struct {
		op Operation
		want NetworkPolicy
	}{
		{Health, Local}, {QueueList, Local}, {QueueEnqueue, Mixed},
		{WatchCheck, Mixed}, {Lookup, Mixed}, {PTRLookup, Mixed},
		{PTRContribute, Online}, {SiteTest, Online}, {RuntimeInstall, Online},
		{StageAllocate, Local}, {StageAbandon, Local},
		{ImportMedia, Local}, {ImageThumbnail, Local}, {SetOffline, Local},
	} {
		rule, ok := RuleFor(tc.op)
		if !ok || rule.Network != tc.want {
			t.Errorf("%s: got %+v, want %s", tc.op, rule, tc.want)
		}
	}
}

func TestEnvelopeTypedControlRoundTrip(t *testing.T) {
	payload, err := json.Marshal(OfflinePolicy{Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(Envelope{Kind: Request, ID: 1, Op: SetOffline, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	var got Envelope
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	var policy OfflinePolicy
	if err := json.Unmarshal(got.Payload, &policy); err != nil {
		t.Fatal(err)
	}
	if got.Kind != Request || got.ID != 1 || got.Op != SetOffline || !policy.Offline || got.Error != nil {
		t.Fatalf("unexpected round trip: %+v %+v", got, policy)
	}
}

func TestBrokerRolesDoNotCrossLibraryBoundary(t *testing.T){
	for _,op:=range []Operation{OperationProbeStart,OperationProbeStop,OperationVersionProbe,OperationStreamStart,OperationStreamRead,OperationStreamFinish,OperationStreamStop,OperationTransferProbeStart}{
		for _,role:=range []Role{Library,Worker,OperationCustodian}{
			if AllowedDirection(role,op){t.Fatalf("broker operation allowed for %s",role)}
		}
	}
	for _,op:=range []Operation{QueueEnqueue,GalleryList,SetOffline,ImportMedia,TransferProbeStart}{
		if AllowedDirection(OperationRequester,op)||AllowedDirection(OperationCustodian,op){t.Fatalf("library operation on broker: %s",op)}
	}
}
