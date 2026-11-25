package acquisitionpolicy

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProfileLockExcludesSecondOwnerAndReopens(t *testing.T){
	dir:=t.TempDir()
	first,err:=LockProfile(dir)
	if err!=nil{t.Fatal(err)}
	defer first.Close()
	second,err:=LockProfile(filepath.Join(dir,"."))
	if second!=nil||!errors.Is(err,ErrProfileOwned){if second!=nil{_ = second.Close()};t.Fatalf("second owner: %v",err)}
	if err:=first.Close();err!=nil{t.Fatal(err)}
	second,err=LockProfile(dir)
	if err!=nil{t.Fatal(err)}
	if err:=second.Close();err!=nil{t.Fatal(err)}
	if _,err:=os.Stat(filepath.Join(dir,lockFile));err!=nil{t.Fatal("persistent lock file missing: ",err)}
}

func TestProfileLockRefusesSymlink(t *testing.T){
	dir:=t.TempDir()
	outside:=filepath.Join(t.TempDir(),"outside")
	if err:=os.WriteFile(outside,[]byte("preserve"),0o600);err!=nil{t.Fatal(err)}
	if err:=os.Symlink(outside,filepath.Join(dir,lockFile));err!=nil{t.Skipf("symlink unavailable: %v",err)}
	l,err:=LockProfile(dir)
	if l!=nil{_ = l.Close()}
	if l!=nil||!errors.Is(err,ErrInvalidPolicy){t.Fatalf("symlink: %v",err)}
	if body,err:=os.ReadFile(outside);err!=nil||string(body)!="preserve"{t.Fatal("outside file changed")}
}
