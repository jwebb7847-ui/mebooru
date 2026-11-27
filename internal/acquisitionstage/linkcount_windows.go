package acquisitionstage

import(
	"os"

	"golang.org/x/sys/windows"
)

func singleLink(f *os.File,_ os.FileInfo)bool{
	var info windows.ByHandleFileInformation
	return windows.GetFileInformationByHandle(windows.Handle(f.Fd()),&info)==nil&&info.NumberOfLinks==1
}
