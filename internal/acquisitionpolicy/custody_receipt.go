package acquisitionpolicy

import (
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/jwebb7847-ui/mebooru/shared/fsx"
)

const cleanupReceiptFile = "acquisition-cleanup.receipt"
const cleanupReceiptPrefix = "mebooru-acquisition-cleanup-v1\n"
const cleanupReceiptBytes = len(cleanupReceiptPrefix)+generationSize*2+1

var ErrInvalidCleanupReceipt = errors.New("invalid managed acquisition cleanup receipt")

func validGeneration(id string) bool {
	if len(id) != generationSize*2 { return false }
	b,err := hex.DecodeString(id)
	if err != nil || hex.EncodeToString(b) != id { return false }
	for _,v := range b { if v != 0 { return true } }
	return false
}

// RecordCustodyStopped is reserved for the trusted short-lived custodian
// after it has irreversibly disabled further launches and verified ALL
// owned children have stopped. It writes no gallery, queue, or policy data.
// It must be the custodian's final state mutation. This is a cooperative
// proof contract, not authorization against other same-user processes.
// A stale writer cannot authorize a different generation. Do not expose
// this API through HTTP, IPC operation dispatch, or operator recovery UI.
func RecordCustodyStopped(profileDir, generation string) error {
	if !filepath.IsAbs(profileDir) || !validGeneration(generation) { return ErrInvalidCleanupReceipt }
	root,err := os.OpenRoot(profileDir)
	if err != nil { return err }
	defer root.Close()
	info,err := root.Lstat(cleanupReceiptFile)
	if err != nil && !errors.Is(err,os.ErrNotExist) { return err }
	if err == nil && !info.Mode().IsRegular() { return ErrInvalidCleanupReceipt }
	return fsx.WriteDurableIn(root,cleanupReceiptFile,func(f *os.File)error{
		_,err:=io.WriteString(f,cleanupReceiptPrefix+generation+"\n")
		return err
	})
}

func verifyCleanupReceipt(root *os.Root,generation string) error {
	if !validGeneration(generation) { return ErrInvalidCleanupReceipt }
	info,err := root.Lstat(cleanupReceiptFile)
	if err != nil { return err }
	if !info.Mode().IsRegular() || info.Size()!=int64(cleanupReceiptBytes) { return ErrInvalidCleanupReceipt }
	f,err := root.Open(cleanupReceiptFile)
	if err != nil { return err }
	defer f.Close()
	opened,err:=f.Stat()
	if err!=nil{return err}
	if !opened.Mode().IsRegular()||!os.SameFile(info,opened){return ErrInvalidCleanupReceipt}
	body,err:=io.ReadAll(io.LimitReader(f,int64(cleanupReceiptBytes)+1))
	if err!=nil{return err}
	if string(body)!=cleanupReceiptPrefix+generation+"\n"{return ErrInvalidCleanupReceipt}
	return nil
}
