package workerproto

// ProbeOperationRef is scoped to a single private custody session. It
// is not an OS process ID, durable job ID or authorization credential.
type ProbeOperationRef struct {
	ID uint64 `json:"id"`
}
func DecodeProbeOperationRef(body []byte)(ProbeOperationRef,error){
	var q ProbeOperationRef
	if err:=decodeObject(body,&q,"id");err!=nil||q.ID==0{return ProbeOperationRef{},ErrMessage}
	return q,nil
}
