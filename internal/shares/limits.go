package shares

import (
	"slices"

	"filebrowser/internal/files"
	"filebrowser/internal/store"
)

// Limits are what a drop link accepts, with the server defaults filled in;
// 0 still means no limit.
type Limits struct {
	Bytes     int64 // in total
	Files     int   // in total
	FileBytes int64 // per file
	Types     []string
}

func (s *Service) Limits(sh *store.Share) Limits {
	l := Limits{Bytes: sh.MaxBytes, Files: sh.MaxFiles, FileBytes: sh.MaxFileBytes, Types: sh.AllowedTypes}
	if l.Bytes == 0 {
		l.Bytes = s.DropMaxBytes
	}
	if l.Files == 0 {
		l.Files = s.DropMaxFiles
	}
	if l.FileBytes == 0 || (s.UploadMaxSize > 0 && l.FileBytes > s.UploadMaxSize) {
		l.FileBytes = s.UploadMaxSize
	}
	return l
}

// Allows reports whether a file of the media type mime may be dropped.
func (l Limits) Allows(mime string) bool {
	return len(l.Types) == 0 || slices.Contains(l.Types, files.Category(mime))
}
