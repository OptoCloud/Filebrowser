//go:build !unix

package files

// renameAt renames the entry at the real path from to the real path to. This
// platform has no rename between two open directories, so both ends are
// checked to be free of symlinks and the rename is then made by path: a link
// swapped in between the two steps would still be followed.
func (f *FS) renameAt(from, to string) error {
	for _, p := range []string{from, to} {
		dir, _, err := f.parent(p)
		if err != nil {
			return err
		}
		_ = dir.Close()
	}
	return f.root.Rename(osPath(from), osPath(to))
}
