package gdl

import (
	"mime"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/contract"
)

func Ingestable(path string) bool {
	return slices.Contains(contract.IngestExtensions, strings.ToLower(filepath.Ext(path)))
}

func ingestableMedia(contentType string) (string, bool) {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", false
	}
	ext, ok := mediaExtensions[mt]
	return ext, ok
}
