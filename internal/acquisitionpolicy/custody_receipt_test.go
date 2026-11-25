package acquisitionpolicy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This helper writes a simulated dirty persisted record without launching
// anything. Existing abrupt owner fixture separately proves OS lock loss.
func seedDirtyGeneration(t *testing.T,dir,id string){
	t.Helper()
	l,err:=LockProfile(dir)
	if err!=nil{t.Fatal(err)}
	defer l.Close()
	var record []byte
	record=append(record,custodyFenceVersion,fenceDirty)
	for i:=0;i<generationSize;i++{record=append(record,id[0])}
	// Tests read the resulting canonical generation from the locked inode.
	if err:=l.writeAtSync(record,1);err!=nil{t.Fatal(err)}
}

func TestCustodyReceiptRecoveryRejectsStaleAndMalformed(t *testing.T){
	dir:=t.TempDir()
	seedDirtyGeneration(t,dir,"a")
	l,err:=LockProfile(dir)
	if err!=nil{t.Fatal(err)}
	defer l.Close()
	record,err:=l.readFence()
	if err!=nil{t.Fatal(err)}
	id:=record.generationID()
	if err:=l.checkRecovery();!errors.Is(err,ErrWorkerRecoveryRequired){t.Fatalf("missing receipt: %v",err)}
	if err:=RecordCustodyStopped(dir,strings.Repeat("b",generationSize*2));err!=nil{t.Fatal(err)}
	if err:=l.checkRecovery();!errors.Is(err,ErrWorkerRecoveryRequired){t.Fatalf("stale receipt: %v",err)}
	if err:=os.WriteFile(filepath.Join(dir,cleanupReceiptFile),[]byte(cleanupReceiptPrefix+id+"\nextra"),0o600);err!=nil{t.Fatal(err)}
	if err:=l.checkRecovery();!errors.Is(err,ErrWorkerRecoveryRequired){t.Fatalf("malformed receipt: %v",err)}
	if err:=RecordCustodyStopped(dir,id);err!=nil{t.Fatal(err)}
	if err:=l.checkRecovery();err!=nil{t.Fatal(err)}
	f,err:=l.BeginWorker()
	if err!=nil{t.Fatal(err)}
	if f.Generation()==id{t.Fatal("reused old generation")}
	if err:=f.ConfirmStopped();err!=nil{t.Fatal(err)}
}

func TestCustodyReceiptNeverClearsLegacyDirty(t *testing.T){
	dir:=t.TempDir()
	if err:=os.WriteFile(filepath.Join(dir,lockFile),[]byte{0,fenceVersion,fenceDirty},0o600);err!=nil{t.Fatal(err)}
	if err:=RecordCustodyStopped(dir,strings.Repeat("a",generationSize*2));err!=nil{t.Fatal(err)}
	l,err:=LockProfile(dir)
	if err!=nil{t.Fatal(err)}
	defer l.Close()
	if err:=l.checkRecovery();!errors.Is(err,ErrWorkerRecoveryRequired){t.Fatalf("legacy dirty recovered: %v",err)}
}

func TestCustodyReceiptRefusesSymlinkAndInvalidGeneration(t *testing.T){
	dir:=t.TempDir()
	outside:=filepath.Join(t.TempDir(),"outside")
	if err:=os.WriteFile(outside,[]byte("preserve"),0o600);err!=nil{t.Fatal(err)}
	if err:=RecordCustodyStopped(dir,"../outside");!errors.Is(err,ErrInvalidCleanupReceipt){t.Fatalf("invalid generation: %v",err)}
	if err:=os.Symlink(outside,filepath.Join(dir,cleanupReceiptFile));err!=nil{t.Skipf("symlink unavailable: %v",err)}
	if err:=RecordCustodyStopped(dir,strings.Repeat("a",generationSize*2));!errors.Is(err,ErrInvalidCleanupReceipt){t.Fatalf("symlink write: %v",err)}
	seedDirtyGeneration(t,dir,"a")
	l,err:=LockProfile(dir)
	if err!=nil{t.Fatal(err)}
	defer l.Close()
	if err:=l.checkRecovery();!errors.Is(err,ErrWorkerRecoveryRequired){t.Fatalf("symlink read: %v",err)}
	if body,err:=os.ReadFile(outside);err!=nil||string(body)!="preserve"{t.Fatal("outside file changed")}
}
