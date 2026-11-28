//go:build !darwin && !linux && !windows

package acquisitionpolicy

import "errors"

func startPlatformCustodyFixture(string)(CustodyChild,error){
	return nil,errors.New("fixture platform unsupported")
}
func runPlatformCustodyFixture(string,string){}
