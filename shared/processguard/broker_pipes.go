package processguard

import "os"

func validateProbePipes(files ...*os.File)error{
	seen:=make(map[uintptr]bool)
	for _,f:=range files{
		if f==nil||seen[f.Fd()]{return ErrInheritedPipes}
		seen[f.Fd()]=true
		info,err:=f.Stat()
		if err!=nil||info.Mode()&os.ModeNamedPipe==0{return ErrInheritedPipes}
	}
	return nil
}
