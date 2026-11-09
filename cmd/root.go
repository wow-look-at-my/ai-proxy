// Package cmd holds the ai-proxy subcommands. Each file registers its own.
package cmd

import (
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version names the build: the module version, then the commit it was built from.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return info.Main.Version + " (" + setting.Value + ")"
		}
	}
	return info.Main.Version
}

var rootCmd = &cobra.Command{
	Use:     "ai-proxy",
	Version: version(),
	Short: "Byte-for-byte relay for LLM APIs that shrinks uploads with zstd",
	Long: "ai-proxy forwards LLM API requests to their upstream without parsing them.\n" +
		"It decodes zstd request bodies, including deltas against the previous\n" +
		"request, so a client on a slow link uploads only what changed.",
	SilenceUsage: true,
}

// Execute runs the root command and exits non-zero on failure.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
