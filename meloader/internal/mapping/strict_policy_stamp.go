package mapping

import "github.com/monbooru/monloader/internal/config"

// StrictPolicyStamp pins the immutable local config and profile snapshots
// used by the existing strict evaluator. It is not a persistent policy
// generation. The owner must separately track all policy changes,
// including custom categories and external authority changes.
type StrictPolicyStamp struct {
	owner *Mapper
	cfg *config.Config
	profiles *profileSet
}

func(m *Mapper) CaptureStrictPolicy() StrictPolicyStamp {
	if m==nil||m.cfg==nil{return StrictPolicyStamp{}}
	return StrictPolicyStamp{owner:m,cfg:m.cfg.Current(),profiles:m.view()}
}

func(m *Mapper) StrictPolicyMatches(s StrictPolicyStamp)bool{
	return m!=nil&&s.owner==m&&s.cfg!=nil&&s.profiles!=nil&&
		m.cfg.Current()==s.cfg&&m.view()==s.profiles
}
