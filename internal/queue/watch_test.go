package queue

import (
	"testing"

	"github.com/monbooru/monloader/internal/outcome"
)

func TestWatchRetryPreservesStrictTaxonomyRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, code, message string
		want                bool
	}{
		{"taxonomy 429", outcome.ErrCodeRateLimited, outcome.StrictTaxonomyPrefix + "HTTP 429", true},
		{"taxonomy 503", outcome.ErrCodeDownloadFailed, outcome.StrictTaxonomyPrefix + "HTTP 503", true},
		{"old taxonomy row", outcome.ErrCodeMappingFailed, outcome.StrictTaxonomyPrefix + "HTTP 429", true},
		{"media 429", outcome.ErrCodeRateLimited, "HTTP 429 from gallery-dl", false},
		{"media 503", outcome.ErrCodeDownloadFailed, "HTTP 503 from gallery-dl", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := &Job{}
			job.Items = []Item{{ErrorCode: tc.code, Error: tc.code + ": " + tc.message}}
			if got := job.owesArchivedPost(); got != tc.want {
				t.Errorf("ordinary watch recovery = %t, want %t", got, tc.want)
			}
			job.Force = true
			if job.owesArchivedPost() {
				t.Error("forced watch recovery should not requeue the same refusal")
			}
		})
	}
}
