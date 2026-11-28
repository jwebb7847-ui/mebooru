package workerproto

// Persisted consent is written only by the library owner. A contained
// policy subprocess can independently verify the exact online record.
const AcquisitionPolicyFile = "acquisition-policy"
const AcquisitionOnlineRecord = "mebooru-acquisition-policy-v1\nonline\n"

// OfflineAck is a typed control acknowledgement. Drained must be true
// for offline: the real worker must wait for all online operations and
// owned descendants before sending it. Decoding an ack is not proof that
// the sender fulfilled that contract.
type OfflineAck struct {
	Offline bool `json:"offline"`
	Drained bool `json:"drained"`
}

type IdleReservation struct {
	Reserved bool `json:"reserved"`
}

func DecodeOfflinePolicy(body []byte)(OfflinePolicy,error){
	var wire struct{ Offline *bool `json:"offline"` }
	if err:=decodeObject(body,&wire,"offline");err!=nil||wire.Offline==nil{
		return OfflinePolicy{},ErrMessage
	}
	return OfflinePolicy{Offline:*wire.Offline},nil
}

func DecodeOfflineAck(body []byte)(OfflineAck,error){
	var wire struct{
		Offline *bool `json:"offline"`
		Drained *bool `json:"drained"`
	}
	if err:=decodeObject(body,&wire,"offline","drained");err!=nil||wire.Offline==nil||wire.Drained==nil{
		return OfflineAck{},ErrMessage
	}
	return OfflineAck{Offline:*wire.Offline,Drained:*wire.Drained},nil
}

func DecodeIdleReservation(body []byte)(IdleReservation,error){
	var wire struct{ Reserved *bool `json:"reserved"` }
	if err:=decodeObject(body,&wire,"reserved");err!=nil||wire.Reserved==nil{
		return IdleReservation{},ErrMessage
	}
	return IdleReservation{Reserved:*wire.Reserved},nil
}
