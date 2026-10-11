package files

import (
	"errors"
	"fmt"
	"io/fs"

	"golang.org/x/sys/windows"
)

// deviceID is not available from a Windows FileInfo; callers fall back to
// copying when a rename crosses volumes.
func deviceID(fs.FileInfo) (uint64, bool) { return 0, false }

// fileID identifies the file at p (real path) by volume serial number and
// file index.
func (f *FS) fileID(p string) (string, error) {
	file, err := f.root.Open(osPath(p))
	if err != nil {
		return "", mapErr(err)
	}
	defer file.Close()
	var d windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &d); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x:%x%08x", d.VolumeSerialNumber, d.FileIndexHigh, d.FileIndexLow), nil
}

// errCrossDevice is what a rename across volumes fails with (for tests).
var errCrossDevice error = windows.ERROR_NOT_SAME_DEVICE

// isCrossDevice reports a rename that failed because it crossed volumes.
func isCrossDevice(err error) bool { return errors.Is(err, windows.ERROR_NOT_SAME_DEVICE) }
