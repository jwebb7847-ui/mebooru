package acquisitionpolicy

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeStore struct {
	offline bool
	loadErr error
	saveErr error
	log *[]string
}
func (s *fakeStore) Load()(bool,error){return s.offline,s.loadErr}
func (s *fakeStore) Save(offline bool)error{
	if offline{*s.log=append(*s.log,"save offline")}else{*s.log=append(*s.log,"save online")}
	if s.saveErr!=nil{return s.saveErr}
	s.offline=offline
	return nil
}
type fakePeer struct{
	log *[]string
	applyErr error
	stopErr error
	beforeApply func(bool)
	beforeStop func(context.Context)
}
func(p *fakePeer)ApplyOffline(_ context.Context,offline bool)error{
	if p.beforeApply!=nil{p.beforeApply(offline)}
	if offline{*p.log=append(*p.log,"apply offline")}else{*p.log=append(*p.log,"apply online")}
	return p.applyErr
}
func(p *fakePeer)Stop(ctx context.Context)error{
	if p.beforeStop!=nil{p.beforeStop(ctx)}
	*p.log=append(*p.log,"stop")
	return p.stopErr
}
func checkLog(t *testing.T,got []string,want ...string){
	t.Helper()
	if !reflect.DeepEqual(got,want){t.Fatalf("transition order %v, want %v",got,want)}
}

func TestCoordinatorPersistBeforeGrant(t *testing.T){
	ctx:=context.Background()
	var log []string
	store:=&fakeStore{offline:true,log:&log}
	c,err:=newCoordinator(store)
	if err!=nil||c.EffectiveOnline(){t.Fatal("initial policy")}
	peer:=&fakePeer{log:&log,beforeApply:func(offline bool){
		if c.EffectiveOnline(){t.Fatal("admissions opened before acknowledgement")}
		if store.offline!=offline{t.Fatal("policy applied before saving")}
	}}
	if err:=c.Attach(ctx,peer);err!=nil{t.Fatal(err)}
	if err:=c.SetOffline(ctx,false);err!=nil{t.Fatal(err)}
	if !c.EffectiveOnline()||c.ConfiguredOffline(){t.Fatal("online not granted")}
	if err:=c.SetOffline(ctx,true);err!=nil{t.Fatal(err)}
	if c.EffectiveOnline()||!c.ConfiguredOffline(){t.Fatal("offline not enforced")}
	checkLog(t,log,"apply offline","save online","apply online","save offline","apply offline")
	if err:=c.Release(ctx);err!=nil{t.Fatal(err)}
	reloaded,err:=newCoordinator(store)
	if err!=nil||!reloaded.ConfiguredOffline()||reloaded.EffectiveOnline(){t.Fatal("restart did not preserve offline")}
}

func TestCoordinatorWriteFailureStopsAndDenies(t *testing.T){
	ctx,cancel:=context.WithCancel(context.Background())
	defer cancel()
	var log []string
	store:=&fakeStore{offline:false,log:&log}
	c,_:=newCoordinator(store)
	p:=&fakePeer{log:&log,beforeStop:func(cleanup context.Context){
		if cleanup.Err()!=nil{t.Fatal("cleanup inherited canceled request")}
		if c.EffectiveOnline(){t.Fatal("failed save kept admission open")}
	}}
	if err:=c.Attach(ctx,p);err!=nil{t.Fatal(err)}
	store.saveErr=errors.New("disk write failed")
	cancel()
	if err:=c.SetOffline(ctx,true);!errors.Is(err,store.saveErr){t.Fatalf("save: %v",err)}
	if c.EffectiveOnline()||!c.ConfiguredOffline(){t.Fatal("write failure did not fail closed")}
	if store.offline{t.Fatal("test store falsely reports persisted offline")}
	checkLog(t,log,"apply online","save offline","stop")
}

func TestCoordinatorFailedCleanupRetainsOwnership(t *testing.T){
	ctx:=context.Background()
	var log []string
	store:=&fakeStore{offline:true,log:&log}
	c,_:=newCoordinator(store)
	p:=&fakePeer{log:&log}
	if err:=c.Attach(ctx,p);err!=nil{t.Fatal(err)}
	p.applyErr=errors.New("lost session")
	p.stopErr=errors.New("cleanup failed")
	if err:=c.SetOffline(ctx,false);!errors.Is(err,p.applyErr)||!errors.Is(err,p.stopErr){t.Fatalf("failure: %v",err)}
	if c.EffectiveOnline(){t.Fatal("failed apply opened admission")}
	if err:=c.Attach(ctx,&fakePeer{log:&log});!errors.Is(err,ErrPeerAttached){t.Fatalf("second peer: %v",err)}
	p.stopErr=nil
	if err:=c.Release(ctx);err!=nil{t.Fatal(err)}
	if err:=c.Attach(ctx,&fakePeer{log:&log});err!=nil{t.Fatal(err)}
	if !c.EffectiveOnline(){t.Fatal("explicit persisted online not restored on new session")}
}

func TestCoordinatorMalformedRecordAttachesOffline(t *testing.T){
	var log []string
	store:=&fakeStore{offline:false,loadErr:ErrInvalidPolicy,log:&log}
	c,err:=newCoordinator(store)
	if !errors.Is(err,ErrInvalidPolicy)||!c.ConfiguredOffline(){t.Fatal("load did not fail closed")}
	if err:=c.Attach(context.Background(),&fakePeer{log:&log});err!=nil{t.Fatal(err)}
	if c.EffectiveOnline(){t.Fatal("malformed record enabled network")}
	checkLog(t,log,"apply offline")
}
