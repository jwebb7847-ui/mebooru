package acquisitionpolicy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openTestStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPolicyDefaultOfflineAndPersistence(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t,dir)
	if offline,err := s.Load(); !offline || err!=nil { t.Fatalf("fresh: %v %v",offline,err) }
	if _,err:=os.Stat(filepath.Join(dir,policyFile));!errors.Is(err,os.ErrNotExist){t.Fatalf("read created policy: %v",err)}
	if err:=s.Save(false);err!=nil{t.Fatal(err)}
	reopened:=openTestStore(t,dir)
	if offline,err:=reopened.Load();offline||err!=nil{t.Fatalf("saved online: %v %v",offline,err)}
	if err:=reopened.Save(true);err!=nil{t.Fatal(err)}
	again:=openTestStore(t,dir)
	if offline,err:=again.Load();!offline||err!=nil{t.Fatalf("saved offline: %v %v",offline,err)}
	body,err:=os.ReadFile(filepath.Join(dir,policyFile))
	if err!=nil||string(body)!=offlineRecord{t.Fatalf("unexpected policy file: %q %v",body,err)}
}

func TestPolicyMalformedNeverEnablesOnline(t *testing.T) {
	for _,body:=range []string{"", "online", "mebooru-acquisition-policy-v2\nonline\n",
		onlineRecord+offlineRecord, onlineRecord+" ", strings.Repeat("x",maxRecordBytes+1)}{
		dir:=t.TempDir()
		if err:=os.WriteFile(filepath.Join(dir,policyFile),[]byte(body),0o600);err!=nil{t.Fatal(err)}
		s:=openTestStore(t,dir)
		if offline,err:=s.Load();!offline||!errors.Is(err,ErrInvalidPolicy){t.Fatalf("invalid record enabled policy: %v %v",offline,err)}
		if err:=s.Save(true);err!=nil{t.Fatal(err)}
		if offline,err:=s.Load();!offline||err!=nil{t.Fatalf("explicit repair: %v %v",offline,err)}
	}
}

func TestPolicyRefusesNonregularEntries(t *testing.T) {
	dir:=t.TempDir()
	path:=filepath.Join(dir,policyFile)
	if err:=os.Mkdir(path,0o700);err!=nil{t.Fatal(err)}
	s:=openTestStore(t,dir)
	if offline,err:=s.Load();!offline||!errors.Is(err,ErrInvalidPolicy){t.Fatalf("directory: %v %v",offline,err)}
	if err:=s.Save(false);!errors.Is(err,ErrInvalidPolicy){t.Fatalf("save over directory: %v",err)}
	if info,err:=os.Stat(path);err!=nil||!info.IsDir(){t.Fatalf("directory overwritten: %v",err)}
}

func TestPolicyRefusesSymlink(t *testing.T) {
	dir:=t.TempDir()
	outside:=filepath.Join(t.TempDir(),"consent")
	if err:=os.WriteFile(outside,[]byte(onlineRecord),0o600);err!=nil{t.Fatal(err)}
	if err:=os.Symlink(outside,filepath.Join(dir,policyFile));err!=nil{t.Skipf("symlink unavailable: %v",err)}
	s:=openTestStore(t,dir)
	if offline,err:=s.Load();!offline||!errors.Is(err,ErrInvalidPolicy){t.Fatalf("symlink: %v %v",offline,err)}
	if err:=s.Save(true);!errors.Is(err,ErrInvalidPolicy){t.Fatalf("symlink save: %v",err)}
	body,err:=os.ReadFile(outside)
	if err!=nil||string(body)!=onlineRecord{t.Fatal("outside file modified")}
}

func TestPolicyInvalidRoot(t *testing.T) {
	if s,err:=Open("relative");s!=nil||err==nil{t.Fatal("relative root accepted")}
	if s,err:=Open(filepath.Join(t.TempDir(),"absent"));s!=nil||err==nil{t.Fatal("missing root accepted")}
}
