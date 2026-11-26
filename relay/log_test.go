package relay

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/ai-proxy/zdelta"
)

// logs is a slog.Handler that hands every record to the test.
type logs chan map[string]any

func (l logs) Enabled(context.Context, slog.Level) bool { return true }

func (l logs) Handle(_ context.Context, r slog.Record) error {
	line := map[string]any{"msg": r.Message, "level": r.Level}
	r.Attrs(func(a slog.Attr) bool {
		line[a.Key] = a.Value.Any()
		return true
	})
	l <- line
	return nil
}

func (l logs) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l logs) WithGroup(string) slog.Handler      { return l }

// request returns the next "request" line. The relay writes it after the
// response ends, so the test must wait for it.
func (l logs) request(t *testing.T) map[string]any {
	t.Helper()
	for {
		select {
		case line := <-l:
			if line["msg"] == "request" {
				return line
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the relay logged no request line")
		}
	}
}

func newLoggedRelay(t *testing.T, up *upstream) (string, logs) {
	t.Helper()
	l := make(logs, 16)
	srv := newRelay(t, up, func(c *Config) { c.Logger = slog.New(l) })
	return srv.URL, l
}

func TestASuccessfulRequestIsLogged(t *testing.T) {
	up := newUpstream(t, nil)
	url, l := newLoggedRelay(t, up)
	body := []byte(`{"model":"m"}`)

	resp := post(t, url+"/anthropic/v1/messages?key=sk-in-the-query", body, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	line := l.request(t)
	assert.Equal(t, slog.LevelInfo, line["level"])
	assert.Equal(t, http.MethodPost, line["method"])
	assert.Equal(t, "/anthropic/v1/messages", line["path"], "the query can hold an API key")
	assert.EqualValues(t, http.StatusOK, line["status"])
	assert.Equal(t, "plain", line["upload"])
	assert.EqualValues(t, len(body), line["wire_bytes"])
	assert.NotContains(t, line, "plain_bytes", "a plain upload has one size")
	assert.EqualValues(t, len(`{"ok":true}`), line["response_bytes"])
	assert.IsType(t, time.Duration(0), line["duration"])
	for key, value := range line {
		if text, ok := value.(string); ok {
			assert.NotContains(t, text, "sk-in-the-query", key)
		}
	}
}

func TestTheLogLineShowsWhatADeltaSaved(t *testing.T) {
	up := newUpstream(t, nil)
	url, l := newLoggedRelay(t, up)
	first := bytes.Repeat([]byte(`{"role":"user","content":"turn"},`), 500)
	second := append(append([]byte(nil), first...), []byte(`{"role":"user","content":"new turn"}`)...)
	headers := map[string]string{"Content-Encoding": "zstd", zdelta.DeltaHeader: zdelta.DeltaValue, "X-Api-Key": "sk-test"}

	frame, err := zdelta.Encode(first, nil)
	require.NoError(t, err)
	resp := post(t, url+"/anthropic/v1/messages", frame, headers)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	line := l.request(t)
	assert.Equal(t, "zstd", line["upload"])
	assert.EqualValues(t, len(frame), line["wire_bytes"])
	assert.EqualValues(t, len(first), line["plain_bytes"])

	delta, err := zdelta.Encode(second, first)
	require.NoError(t, err)
	headers[zdelta.BaseHeader] = zdelta.Hash(first)
	resp = post(t, url+"/anthropic/v1/messages", delta, headers)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	line = l.request(t)
	assert.Equal(t, "delta", line["upload"])
	assert.EqualValues(t, len(delta), line["wire_bytes"])
	assert.EqualValues(t, len(second), line["plain_bytes"])
}

func TestARefusedRequestIsLoggedAsAWarning(t *testing.T) {
	up := newUpstream(t, nil)
	url, l := newLoggedRelay(t, up)

	resp := post(t, url+"/nowhere/v1/messages", []byte(`{}`), nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	line := l.request(t)
	assert.Equal(t, slog.LevelWarn, line["level"])
	assert.EqualValues(t, http.StatusNotFound, line["status"])

	delta, err := zdelta.Encode([]byte("next"), []byte("a base the relay never saw"))
	require.NoError(t, err)
	resp = post(t, url+"/anthropic/v1/messages", delta, map[string]string{
		"Content-Encoding": "zstd", zdelta.BaseHeader: zdelta.Hash([]byte("a base the relay never saw")),
	})
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	line = l.request(t)
	assert.Equal(t, slog.LevelWarn, line["level"])
	assert.EqualValues(t, http.StatusConflict, line["status"])
	assert.Equal(t, "delta", line["upload"])
}

func TestAnUpstreamErrorStatusIsLoggedAsAWarning(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	url, l := newLoggedRelay(t, up)

	resp := post(t, url+"/anthropic/v1/messages", []byte(`{}`), nil)
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	line := l.request(t)
	assert.Equal(t, slog.LevelWarn, line["level"])
	assert.EqualValues(t, http.StatusTooManyRequests, line["status"])
}
