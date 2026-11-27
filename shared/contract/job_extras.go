package contract

import (
	"slices"
	"strings"
	"unicode"
)

// JobExtras is client-owned submission metadata, not approved source
// evidence. This shared pure projection preserves legacy worker semantics;
// neither projection nor normalization grants acquisition or library writes.
type JobExtras struct {
	Tags []string
	Commentary string
	Source string
	Via string
}
func(e JobExtras)Empty()bool{
	return len(e.Tags)==0&&e.Commentary==""&&e.Source==""&&e.Via==""
}

type JobExtrasProjection struct {
	Fields PostFields
	Via string
	ClientTags []string
}

// ProjectJobExtras retains the source's own commentary and claimed
// identity; client tags never join the source-owned tag slice. A caller
// supplies its own observed custom-category lookup. The owner must still
// independently qualify policy, ownership and rating before mutation.
func ProjectJobExtras(fields PostFields,via,rating string,e JobExtras,sourceClaimed bool,custom func(string)bool)JobExtrasProjection{
	if e.Via!=""{via=e.Via}
	if e.Commentary!=""&&fields.Commentary==""{fields.Commentary=e.Commentary}
	if e.Source!=""&&!sourceClaimed{fields.Source=e.Source}
	return JobExtrasProjection{Fields:fields,Via:via,ClientTags:NormalizeClientTags(e.Tags,rating!="",custom)}
}

func NormalizeClientTags(raw []string,rated bool,custom func(string)bool)[]string{
	var out []string
	for _,r:=range raw{
		t:=NormalizeTagForm(r,custom)
		if t==""||(rated&&strings.HasPrefix(t,"rating:")){continue}
		out=append(out,t)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// NormalizeTagForm preserves the worker's established syntax, including
// unrecognized namespace prefixes as literal general names.
func NormalizeTagForm(tag string,custom func(string)bool)string{
	tag=strings.TrimSpace(tag)
	if prefix,rest,ok:=strings.Cut(tag,":");ok{
		category:=strings.ToLower(prefix)
		if slices.Contains(BuiltinCategories,category)||(custom!=nil&&custom(category)){
			name:=NormalizeTagSpelling(rest)
			if name==""||category=="general"{return name}
			return category+":"+name
		}
	}
	return NormalizeTagSpelling(tag)
}
func NormalizeTagSpelling(name string)string{
	name=strings.ToLower(name)
	var b strings.Builder
	b.Grow(len(name))
	pendingFold:=false
	for _,r:=range name{
		switch{
		case unicode.IsControl(r)||unicode.In(r,unicode.Cf,unicode.Co):
		case unicode.IsSpace(r)||r=='"'||r=='*':
			pendingFold=true
		default:
			if pendingFold&&b.Len()>0{b.WriteByte('_')}
			pendingFold=false
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(),"_")
}
