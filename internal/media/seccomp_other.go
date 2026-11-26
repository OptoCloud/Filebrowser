//go:build !linux

package media

import "os"

func seccompFile() *os.File { return nil }
