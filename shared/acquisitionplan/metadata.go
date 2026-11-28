// Package acquisitionplan constructs narrow gallery-dl command shapes.
// It does not launch processes, grant network access, or trust a runtime.
package acquisitionplan

import (
	"errors"
	"strconv"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

var ErrMetadataPlan = errors.New("metadata plan refused")

// StrictMediaURLKey and StrictSourceArgs are shared with legacy strict
// acquisition. Keep the predicate evidence options identical.
const StrictMediaURLKey = "meloader_verified_media_url"

func StrictSourceArgs() []string {
	return []string{"--child-filter", "False", "-o", "follow=false",
		"-o", "keywords={}", "-o", "keywords-global={}",
		"-o", "metadata-url=" + StrictMediaURLKey, "-o", "metadata-path=false",
		"-o", "metadata-http=false", "-o", "metadata-version=false", "-o", "metadata-extractor=false"}
}

// StrictSource limits the accepted extractor classification. Host alone
// is not source proof; the actual extractor result must be checked.
func StrictSource(site, subcategory string) bool {
	return workerproto.QualifiedMetadataSource(site,subcategory)
}

// CompileMetadata is a pure, config-free candidate command constructor.
// expected must originate from trusted custody-side authorization, not
// the request. Exact comparison prevents the requester from widening
// the granted job, gallery, source, URL, operation, or window. It is
// NOT admission: no durable job scope, runtime identity, config identity,
// classification binding, network lease or containment is established.
// Do not connect this function to real acquisition yet.
//
// Only a future trusted launcher may consume these args. There is no
// executable, config path, raw option, environment or destination input.
func CompileMetadata(expected, request workerproto.MetadataPlanRequest) ([]string, error) {
	if !workerproto.ValidMetadataPlan(expected) ||
		!workerproto.ValidMetadataPlan(request) || expected != request {
		return nil, ErrMetadataPlan
	}
	args := []string{"--config-ignore"}
	switch request.Kind {
	case workerproto.MetadataClassify:
		args = append(args, "-E")
	case workerproto.MetadataStrictResolve:
		args = append(args, "-j")
		args = append(args, StrictSourceArgs()...)
		window := strconv.FormatUint(uint64(request.First), 10) + "-" + strconv.FormatUint(uint64(request.Last), 10)
		args = append(args, "--post-range", window)
	default:
		return nil, ErrMetadataPlan
	}
	return append(args, "--", request.URL), nil
}
