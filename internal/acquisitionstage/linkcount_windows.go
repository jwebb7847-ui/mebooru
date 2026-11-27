package acquisitionstage

import(
	"os"

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
