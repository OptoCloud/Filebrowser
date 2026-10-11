//go:build !unix && !windows

package files

import "io/fs"

func deviceID(fs.FileInfo) (uint64, bool) { return 0, false }

func (f *FS) fileID(string) (string, error) { return "", nil }

var errCrossDevice error

func isCrossDevice(error) bool { return false }
