package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runServe(t *testing.T, args ...string) error {
	t.Helper()
	serveFlags.routes = nil
	serveFlags.upstreamKeys = nil
	t.Cleanup(func() {
		serveFlags.routes = nil
		serveFlags.upstreamKeys = nil
		rootCmd.SetArgs(nil)
	})
	rootCmd.SetArgs(append([]string{"serve"}, args...))
	return rootCmd.Execute()
}

func TestServeRejectsARouteWithoutAnUpstream(t *testing.T) {
	err := runServe(t, "--listen", "127.0.0.1:0", "--route", "anthropic")
	require.Error(t, err)
}

func TestServeRejectsAnUpstreamKeyForNoRoute(t *testing.T) {
	err := runServe(t, "--listen", "127.0.0.1:0", "--route", "anthropic=https://api.anthropic.com", "--upstream-key", "openai=sk-x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no route named openai")
}

func TestServeRejectsAMalformedUpstreamKey(t *testing.T) {
	err := runServe(t, "--listen", "127.0.0.1:0", "--route", "anthropic=https://api.anthropic.com", "--upstream-key", "anthropic")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "want name=KEY")
}

func TestUpstreamKeyEnvName(t *testing.T) {
	assert.Equal(t, "AI_PROXY_UPSTREAM_KEY_OPEN_ROUTER", upstreamKeyEnv("open-router"))
}

// An address no listener can take fails the command after every route,
// the delta store and the handler are set up.
func TestServeFailsWhenItCannotListen(t *testing.T) {
	err := runServe(t,
		"--listen", "127.0.0.1:99999",
		"--route", "anthropic=https://api.anthropic.com",
		"--token", "local-test-token",
		"--upstream-key", "anthropic=sk-test",
		"--verbose",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "serve:")
}
