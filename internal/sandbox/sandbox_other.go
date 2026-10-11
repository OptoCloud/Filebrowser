//go:build !windows

package sandbox

import (
	"fmt"
	"os"
)

func launch([]string) int {
	fmt.Fprintln(os.Stderr, "sandbox: the AppContainer launcher is Windows-only")
	return 125
}
