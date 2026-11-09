package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The org's tool installer runs `ai-proxy --version` to prove the binary starts.
func TestVersionFlagPrintsTheBuild(t *testing.T) {
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetArgs([]string{"--version"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	require.NoError(t, rootCmd.Execute())
	assert.Contains(t, out.String(), "ai-proxy version ")
}
