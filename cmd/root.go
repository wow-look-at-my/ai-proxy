// Package cmd holds the ai-proxy subcommands. Each file registers its own.
package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "ai-proxy",
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
