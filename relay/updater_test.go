package relay

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func get(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// docker-updater sends no credential. A relay with a token must still answer it.
func TestUpdaterHealthAnswersWithoutTheToken(t *testing.T) {
	before := time.Now().Add(-time.Second)
	up := newUpstream(t, nil)
	relay := newRelay(t, up, func(c *Config) { c.Token = "secret" })

	resp := get(t, relay.URL+"/.well-known/docker-updater/health")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body struct {
		Started string `json:"started"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	started, err := time.Parse(time.RFC3339Nano, body.Started)
	require.NoError(t, err, "docker-updater reads started as an RFC 3339 time")
	assert.False(t, started.Before(before), "started is the time of this process")
	assert.Empty(t, up.reqs, "nothing reaches the upstream")
}

func TestUpdaterPreUpdateAllowsAnIdleRelay(t *testing.T) {
	up := newUpstream(t, nil)
	relay := newRelay(t, up, func(c *Config) { c.Token = "secret" })

	resp := get(t, relay.URL+"/.well-known/docker-updater/pre-update")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, up.reqs, "nothing reaches the upstream")
}

func TestUpdaterPreUpdateHoldsWhileARequestIsInFlight(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	relay := newRelay(t, up, nil)

	done := make(chan int, 1)
	go func() {
		resp, err := http.Post(relay.URL+"/anthropic/v1/messages", "application/json", bytes.NewReader([]byte(`{}`)))
		if err != nil {
			done <- 0
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		done <- resp.StatusCode
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the request did not reach the upstream")
	}

	resp := get(t, relay.URL+"/.well-known/docker-updater/pre-update")
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "an update must wait for the request")

	close(release)
	assert.Equal(t, http.StatusOK, <-done)
	// The relay counts the request until its handler returns, which is after the client has the response.
	require.Eventually(t, func() bool {
		resp, err := http.Get(relay.URL + "/.well-known/docker-updater/pre-update")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 10*time.Millisecond, "the relay must allow an update when the request ends")
}

func TestUpdaterUnknownCheckIs404(t *testing.T) {
	up := newUpstream(t, nil)
	relay := newRelay(t, up, func(c *Config) { c.Token = "secret" })

	resp := get(t, relay.URL+"/.well-known/docker-updater/other")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "docker-updater reads 404 as not implemented")
}

func TestARouteCannotHideTheUpdaterChecks(t *testing.T) {
	rt, err := ParseRoute(".well-known=https://example.com")
	require.NoError(t, err)
	_, err = New(Config{Routes: []Route{rt}, MaxBodyBytes: 1024})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reserved")
}

func TestUpdaterChecksAreLogged(t *testing.T) {
	up := newUpstream(t, nil)
	url, l := newLoggedRelay(t, up)

	get(t, url+"/.well-known/docker-updater/pre-update")
	select {
	case line := <-l:
		assert.Equal(t, "update allowed", line["msg"])
	case <-time.After(5 * time.Second):
		t.Fatal("the relay logged nothing for the update check")
	}
}
