package cmd

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

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

type answer struct {
	status int
	body   string
	err    error
}

func fetch(url string) answer {
	resp, err := http.Get(url)
	if err != nil {
		return answer{err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return answer{resp.StatusCode, string(body), err}
}

func refusesConnections(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return true
	}
	_ = conn.Close()
	return false
}

// A stop must not cut a response that is on its way to a client.
func TestAStopWaitsForTheRequestInFlight(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "first half, ")
		w.(http.Flusher).Flush()
		close(entered)
		<-release
		_, _ = io.WriteString(w, "second half")
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	stopped := make(chan error, 1)
	go func() {
		stopped <- serveUntilDone(ctx, server, ln, func() int64 { return 1 }, slog.New(slog.DiscardHandler))
	}()

	got := make(chan answer, 1)
	go func() { got <- fetch("http://" + addr + "/") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the request did not reach the handler")
	}

	stop()
	require.Eventually(t, func() bool { return refusesConnections(addr) }, 5*time.Second, 10*time.Millisecond,
		"a stopped relay must take no new connection")
	select {
	case err := <-stopped:
		t.Fatalf("the relay returned with a request in flight: %v", err)
	default:
	}

	close(release)
	a := <-got
	require.NoError(t, a.err)
	assert.Equal(t, http.StatusOK, a.status)
	assert.Equal(t, "first half, second half", a.body, "the client must get the whole response")
	select {
	case err := <-stopped:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the relay did not return after the last request ended")
	}
}

// SIGTERM is what a container runtime sends. The serve command must end cleanly on it.
func TestServeEndsCleanlyOnSIGTERM(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	stopped := make(chan error, 1)
	go func() {
		stopped <- runServe(t, "--listen", addr, "--route", "anthropic=http://127.0.0.1:1", "--token", "local-test-token")
	}()
	// The command catches the signal before it opens the port.
	require.Eventually(t, func() bool {
		return fetch("http://"+addr+"/.well-known/docker-updater/health").status == http.StatusOK
	}, 5*time.Second, 10*time.Millisecond, "the relay did not start")

	self, err := os.FindProcess(os.Getpid())
	require.NoError(t, err)
	require.NoError(t, self.Signal(syscall.SIGTERM))
	select {
	case err := <-stopped:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the serve command did not end on SIGTERM")
	}
	assert.True(t, refusesConnections(addr), "the port must be closed")
}
