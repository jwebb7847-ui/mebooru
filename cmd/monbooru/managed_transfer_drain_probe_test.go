package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
)

func TestTransferStarterRequiresPersistedGrant(t *testing.T){
	dir:=t.TempDir()
	store,err:=acquisitionpolicy.Open(dir)
	if err!=nil{t.Fatal(err)}
	defer store.Close()
	start:=brokerTransferStarter("not-an-executable-digest",dir)
	check:=func(){
		t.Helper()
		child,err:=start(context.Background())
		if child!=nil||!errors.Is(err,workerproto.ErrOffline){t.Fatalf("offline or invalid consent admitted: %v %v",child,err)}
	}
	check() // missing record
	if err:=store.Save(true);err!=nil{t.Fatal(err)}
	check()
	if err:=os.WriteFile(filepath.Join(dir,"acquisition-policy"),[]byte("unrecognized"),0600);err!=nil{t.Fatal(err)}
	check()
}
