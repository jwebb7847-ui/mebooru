package processguard

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

var ErrProbeExecutable = errors.New("managed probe executable identity refused")

// ProbeSibling resolves only the known sibling of the running executable.
// This is a cooperative test-binary layout check, NOT a signed production
// bundle verification or defense against a same-user executable swap.
// No supplied path, PATH lookup, symlink sibling or shell.
func ProbeSibling(selfName, siblingName string)(string,error){
	if !((selfName=="monbooru"&&siblingName=="monloader")||(selfName=="monloader"&&siblingName=="monbooru")){
		return "",ErrProbeExecutable
	}
	if runtime.GOOS=="windows"{selfName+=".exe";siblingName+=".exe"}
	exe,err:=os.Executable()
	if err!=nil{return "",err}
	exe,err=filepath.EvalSymlinks(exe)
	if err!=nil{return "",err}
	if filepath.Base(exe)!=selfName{return "",ErrProbeExecutable}
	path:=filepath.Join(filepath.Dir(exe),siblingName)
	info,err:=os.Lstat(path)
	if err!=nil{return "",err}
	if !info.Mode().IsRegular()||(runtime.GOOS!="windows"&&info.Mode().Perm()&0111==0){return "",ErrProbeExecutable}
	resolved,err:=filepath.EvalSymlinks(path)
	if err!=nil||resolved!=path{return "",ErrProbeExecutable}
	return path,nil
}
