package relay

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/ai-proxy/zdelta"
)

type seen struct {
	method, path, query string
	header              http.Header
	body                []byte
}

type upstream struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []seen
}

func newUpstream(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *upstream {
	t.Helper()
	u := &upstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.reqs = append(u.reqs, seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), body})
		u.mu.Unlock()
		if handler != nil {
			handler(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *upstream) last(t *testing.T) seen {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	require.NotEmpty(t, u.reqs, "the upstream got no request")
	return u.reqs[len(u.reqs)-1]
}

func newRelay(t *testing.T, up *upstream, mutate func(*Config)) *httptest.Server {
	t.Helper()
	rt, err := ParseRoute("anthropic=" + up.URL + "/base")
	require.NoError(t, err)
	cfg := Config{Routes: []Route{rt}, MaxBodyBytes: 1 << 20, Store: zdelta.NewStore(1 << 22)}
	if mutate != nil {
		mutate(&cfg)
	}
	h, err := New(cfg)
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func post(t *testing.T, url string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// The upstream must get exactly what the client sent: same bytes, path,
// query and headers, minus the relay's own headers.
func TestPlainRequestPassesThroughUnchanged(t *testing.T) {
	up := newUpstream(t, nil)
	relay := newRelay(t, up, nil)
	body := []byte(`{"model":"m","unknown_future_field":{"x":[1,2,3]},"stream":true}`)

	resp := post(t, relay.URL+"/anthropic/v1/messages?beta=true", body, map[string]string{
		"Authorization":     "Bearer sk-test",
		"Anthropic-Version": "2023-06-01",
		"X-Custom":          "kept",
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	got := up.last(t)
	assert.Equal(t, body, got.body)
	assert.Equal(t, "/base/v1/messages", got.path)
	assert.Equal(t, "beta=true", got.query)
	assert.Equal(t, "Bearer sk-test", got.header.Get("Authorization"))
	assert.Equal(t, "2023-06-01", got.header.Get("Anthropic-Version"))
	assert.Equal(t, "kept", got.header.Get("X-Custom"))
	assert.Empty(t, got.header.Get("X-Forwarded-For"), "the relay must not reveal the client address")
	assert.Equal(t, "zstd", resp.Header.Get("Accept-Encoding"))
	assert.Equal(t, zdelta.DeltaValue, resp.Header.Get(zdelta.DeltaHeader))
}

func TestZstdBodyReachesTheUpstreamDecoded(t *testing.T) {
	up := newUpstream(t, nil)
	relay := newRelay(t, up, nil)
	plain := bytes.Repeat([]byte(`{"role":"user","content":"hello"},`), 200)
	frame, err := zdelta.Encode(plain, nil)
	require.NoError(t, err)

	resp := post(t, relay.URL+"/anthropic/v1/messages", frame, map[string]string{"Content-Encoding": "zstd"})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	got := up.last(t)
	assert.Equal(t, plain, got.body)
	assert.Empty(t, got.header.Get("Content-Encoding"))
	assert.Equal(t, int64(len(plain)), int64(len(got.body)))
}

// The full protocol: keep the first body, send the second as a delta against
// it, and resend without a base when the relay lost it.
func TestDeltaAgainstAKeptBody(t *testing.T) {
	up := newUpstream(t, nil)
	relay := newRelay(t, up, nil)
	first := bytes.Repeat([]byte(`{"role":"user","content":"turn"},`), 500)
	second := append(append([]byte(nil), first...), []byte(`{"role":"user","content":"new turn"}`)...)

	frame, err := zdelta.Encode(first, nil)
	require.NoError(t, err)
	resp := post(t, relay.URL+"/anthropic/v1/messages", frame, map[string]string{
		"Content-Encoding": "zstd", zdelta.DeltaHeader: zdelta.DeltaValue,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	delta, err := zdelta.Encode(second, first)
	require.NoError(t, err)
	resp = post(t, relay.URL+"/anthropic/v1/messages", delta, map[string]string{
		"Content-Encoding": "zstd", zdelta.DeltaHeader: zdelta.DeltaValue, zdelta.BaseHeader: zdelta.Hash(first),
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got := up.last(t)
	assert.Equal(t, second, got.body)
	assert.Empty(t, got.header.Get(zdelta.BaseHeader), "relay headers stay on the relay")
	assert.Empty(t, got.header.Get(zdelta.DeltaHeader))
	assert.Less(t, len(delta), len(second)/20, "the delta must be a small fraction of the body")
}

func TestUnknownBaseAsksForAResend(t *testing.T) {
	up := newUpstream(t, nil)
	relay := newRelay(t, up, nil)
	delta, err := zdelta.Encode([]byte("next"), []byte("a base the relay never saw"))
	require.NoError(t, err)

	resp := post(t, relay.URL+"/anthropic/v1/messages", delta, map[string]string{
		"Content-Encoding": "zstd", zdelta.BaseHeader: zdelta.Hash([]byte("a base the relay never saw")),
	})
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, "1", resp.Header.Get(zdelta.BaseMissingHeader))
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), `"zstd_base_missing"`)
	assert.Empty(t, up.reqs, "nothing reaches the upstream")
}

func TestStreamedResponseIsFlushedAsItArrives(t *testing.T) {
	release := make(chan struct{})
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	})
	relay := newRelay(t, up, nil)

	resp := post(t, relay.URL+"/anthropic/v1/messages", []byte(`{"stream":true}`), nil)
	reader := bufio.NewReader(resp.Body)
	line := make(chan string, 1)
	go func() {
		l, _ := reader.ReadString('\n')
		line <- l
	}()
	select {
	case l := <-line:
		assert.Equal(t, "event: ping\n", l)
	case <-time.After(5 * time.Second):
		t.Fatal("the first event did not arrive before the upstream finished")
	}
	close(release)
	rest, _ := io.ReadAll(reader)
	assert.Contains(t, string(rest), "message_stop")
}

func TestUpstreamErrorsPassThrough(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.Header().Set("X-Should-Retry", "true")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	})
	relay := newRelay(t, up, nil)

	resp := post(t, relay.URL+"/anthropic/v1/messages", []byte(`{}`), nil)
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, "7", resp.Header.Get("Retry-After"))
	assert.Equal(t, "true", resp.Header.Get("X-Should-Retry"))
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, string(body))
}

func TestTokenIsRequiredAndNotForwarded(t *testing.T) {
	up := newUpstream(t, nil)
	relay := newRelay(t, up, func(c *Config) { c.Token = "secret" })

	resp := post(t, relay.URL+"/anthropic/v1/messages", []byte(`{}`), nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, "zstd", resp.Header.Get("Accept-Encoding"), "an error still advertises")

	resp = post(t, relay.URL+"/anthropic/v1/messages", []byte(`{}`), map[string]string{TokenHeader: "secret"})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, up.last(t).header.Get(TokenHeader))
}

func TestUnknownPrefixIs404(t *testing.T) {
	up := newUpstream(t, nil)
	relay := newRelay(t, up, nil)
	resp := post(t, relay.URL+"/openai/v1/chat/completions", []byte(`{}`), nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Empty(t, up.reqs)
}

func TestBodyLimitApplies(t *testing.T) {
	up := newUpstream(t, nil)
	relay := newRelay(t, up, func(c *Config) { c.MaxBodyBytes = 64 })
	big := bytes.Repeat([]byte("x"), 1000)
	frame, err := zdelta.Encode(big, nil)
	require.NoError(t, err)

	resp := post(t, relay.URL+"/anthropic/v1/messages", frame, map[string]string{"Content-Encoding": "zstd"})
	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode, "a small frame must not decode past the limit")
	assert.Empty(t, up.reqs)
}

func TestParseRoute(t *testing.T) {
	rt, err := ParseRoute("anthropic=https://api.anthropic.com")
	require.NoError(t, err)
	assert.Equal(t, "/anthropic", rt.Prefix)
	assert.Equal(t, "api.anthropic.com", rt.Upstream.Host)

	for _, bad := range []string{"", "noequals", "=https://x", "a=relative/path"} {
		_, err := ParseRoute(bad)
		assert.Error(t, err, bad)
	}
}

func TestLongestPrefixWins(t *testing.T) {
	a := newUpstream(t, nil)
	b := newUpstream(t, nil)
	ra, err := ParseRoute("x=" + a.URL)
	require.NoError(t, err)
	rb, err := ParseRoute("x/y=" + b.URL)
	require.NoError(t, err)
	h, err := New(Config{Routes: []Route{ra, rb}, MaxBodyBytes: 1024})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	post(t, srv.URL+"/x/y/v1/messages", []byte(`{}`), nil)
	assert.Equal(t, "/v1/messages", b.last(t).path)
	assert.Empty(t, a.reqs)
}
