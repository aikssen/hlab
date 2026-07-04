//go:build !windows

package cmd

import (
	"os"
	"syscall"
)

// execSSH replaces the current process with the ssh session. On Unix this is a
// real exec(2), so ssh inherits hlab's terminal, signals and exit status
// directly. bin is the resolved ssh path; argv[0] is "ssh".
func execSSH(bin string, argv []string) error {
	return syscall.Exec(bin, argv, os.Environ())
}
