package acquisitionstage

import(
	"os"

	"golang.org/x/sys/windows"
)

func singleLink(f *os.File,_ os.FileInfo)bool{
	var info windows.ByHandleFileInformation
	return windows.GetFileInformationByHandle(windows.Handle(f.Fd()),&info)==nil&&info.NumberOfLinks==1
}

// Volume serial is a point-in-time check, not a durable globally unique
// volume identifier. Unsupported or unidentifiable volumes refuse.
func sameVolume(a,b *os.File)bool{
	var x,y windows.ByHandleFileInformation
	return windows.GetFileInformationByHandle(windows.Handle(a.Fd()),&x)==nil&&
		windows.GetFileInformationByHandle(windows.Handle(b.Fd()),&y)==nil&&
		x.VolumeSerialNumber!=0&&x.VolumeSerialNumber==y.VolumeSerialNumber
}
