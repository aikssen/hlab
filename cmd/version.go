package cmd

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Version is the released version. It can be overridden at build time with:
//
//	go build -ldflags "-X github.com/aikssen/hlab/cmd.Version=v0.1.0"
var Version = "v0.10.5"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the hlab version and build",
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Println(versionString())
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}

// versionString returns e.g. "hlab v0.1.0-1a2b3c4" (or "-dirty" when the working
// tree had uncommitted changes at build time).
func versionString() string {
	return "hlab " + fullVersion()
}

// fullVersion returns the version plus the build commit, e.g. "v0.1.0-1a2b3c4"
// — the same string `hlab version` prints, minus the leading "hlab ". Also
// shown in the TUI title bar.
func fullVersion() string {
	v := Version
	if c := buildCommit(); c != "" {
		v += "-" + c
	}
	return v
}

func buildCommit() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var rev string
	var dirty bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return ""
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if dirty {
		rev += "-dirty"
	}
	return rev
}
