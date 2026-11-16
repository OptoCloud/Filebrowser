//go:build unix && !linux

package files

import "golang.org/x/sys/unix"

func renameat(fromFD int, from string, toFD int, to string) error {
	return unix.Renameat(fromFD, from, toFD, to)
}
