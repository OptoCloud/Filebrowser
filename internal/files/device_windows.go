package files

import (
	"errors"
	"io/fs"

	"golang.org/x/sys/windows"
)

// deviceID is not available from a Windows FileInfo; callers fall back to
// copying when a rename crosses volumes.
func deviceID(fs.FileInfo) (uint64, bool) { return 0, false }

// errCrossDevice is what a rename across volumes fails with (for tests).
var errCrossDevice error = windows.ERROR_NOT_SAME_DEVICE

// isCrossDevice reports a rename that failed because it crossed volumes.
func isCrossDevice(err error) bool { return errors.Is(err, windows.ERROR_NOT_SAME_DEVICE) }
