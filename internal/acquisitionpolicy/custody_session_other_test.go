//go:build !darwin && !linux && !windows

package acquisitionpolicy

import("os";"errors")

func startSessionFixtureWorker(string,*os.File,*os.File)(CustodyChild,error){return nil,errors.New("unsupported platform")}
func runSessionFixtureWorker(string,string){}
