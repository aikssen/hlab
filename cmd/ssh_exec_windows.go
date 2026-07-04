//go:build windows

package cmd

import (
	"os"
	"os/exec"
)

// execSSH runs the ssh session as a child with hlab's stdio, then returns when
// it exits. Windows has no exec(2) replacement (syscall.Exec returns EWINDOWS),
// so we mirror the portable pattern the dashboard already uses for `s`-key ssh.
// bin is the resolved ssh.exe path; argv[0] is "ssh", so pass argv[1:] as args.
func execSSH(bin string, argv []string) error {
	c := exec.Command(bin, argv[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}
