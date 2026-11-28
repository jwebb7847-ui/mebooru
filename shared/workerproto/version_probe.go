package workerproto

import "regexp"

// VersionProbeResult is diagnostic-only and carries no executable path,
// arguments, credentials or arbitrary stdout. It is not a qualification
// verdict. The broker sends it only after confirmed operation cleanup.
type VersionProbeResult struct {
	Version string `json:"version"`
}
var probeVersionPattern=regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}$`)
func DecodeVersionProbeResult(body []byte)(VersionProbeResult,error){
	var v VersionProbeResult
	if err:=decodeObject(body,&v,"version");err!=nil||len(v.Version)>64||!probeVersionPattern.MatchString(v.Version){return VersionProbeResult{},ErrMessage}
	return v,nil
}
