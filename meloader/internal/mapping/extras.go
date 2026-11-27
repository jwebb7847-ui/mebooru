package mapping

import "github.com/jwebb7847-ui/mebooru/shared/contract"

// Extras is a client's metadata for a submission; every item the job
// pushes carries it. The pure projection is shared with the library.
type Extras = contract.JobExtras

func MergeExtras(pf PushFields, e Extras, sourceClaimed bool) PushFields {
	p:=contract.ProjectJobExtras(pf.PostFields,pf.Via,pf.Rating,e,sourceClaimed,customCategory)
	pf.PostFields= p.Fields
	pf.Via=p.Via
	pf.ClientTags=p.ClientTags
	return pf
}

func clientTags(raw []string,rated bool)[]string{
	return contract.NormalizeClientTags(raw,rated,customCategory)
}
