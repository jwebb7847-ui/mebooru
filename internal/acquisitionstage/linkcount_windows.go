package acquisitionstage

import(
	"os"
	"fmt"

	"golang.org/x/sys/windows"
)

func singleLink(f *os.File,info os.FileInfo)bool{return exactLinks(f,info,1)}
func exactLinks(f *os.File,_ os.FileInfo,n uint64)bool{
	var info windows.ByHandleFileInformation
	return windows.GetFileInformationByHandle(windows.Handle(f.Fd()),&info)==nil&&uint64(info.NumberOfLinks)==n
}

// Volume serial is a point-in-time check, not a durable globally unique
// volume identifier. Unsupported or unidentifiable volumes refuse.
func sameVolume(a,b *os.File)bool{
	var x,y windows.ByHandleFileInformation
	return windows.GetFileInformationByHandle(windows.Handle(a.Fd()),&x)==nil&&
		windows.GetFileInformationByHandle(windows.Handle(b.Fd()),&y)==nil&&
		x.VolumeSerialNumber!=0&&x.VolumeSerialNumber==y.VolumeSerialNumber
}

func diskFileIdentity(f *os.File,_ os.FileInfo)(string,bool){
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(windows.Handle(f.Fd()),&info)!=nil||info.VolumeSerialNumber==0||
		info.FileIndexHigh==0&&info.FileIndexLow==0{return "",false}
	return fmt.Sprintf("windows:%08x:%08x%08x",info.VolumeSerialNumber,info.FileIndexHigh,info.FileIndexLow),true
}
