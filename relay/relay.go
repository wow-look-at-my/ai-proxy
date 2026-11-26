// Package relay forwards requests to an upstream byte for byte. It never
// parses a body: it only undoes the zstd encoding a client applied.
package relay

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wow-look-at-my/ai-proxy/zdelta"
)

// TokenHeader carries the relay's own access token.
const TokenHeader = "X-AI-Proxy-Token"

// Route sends requests under Prefix to Upstream, with Prefix removed.
type Route struct {
	Prefix   string
	Upstream *url.URL
}

// ParseRoute reads "name=https://host/base". Requests to /name/... go to
// https://host/base/...
func ParseRoute(spec string) (Route, error) {
	name, raw, ok := strings.Cut(spec, "=")
	name = strings.Trim(strings.TrimSpace(name), "/")
	if !ok || name == "" {
		return Route{}, fmt.Errorf("route %q: want name=https://host/base", spec)
	}
	upstream, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		return Route{}, fmt.Errorf("route %q: upstream must be an absolute URL", spec)
	}
	return Route{Prefix: "/" + name, Upstream: upstream}, nil
}

// Config builds a Handler.
type Config struct {
	Routes []Route
	// MaxBodyBytes caps a request body, encoded and decoded.
	MaxBodyBytes int64
	// Store keeps bodies for deltas. Nil turns deltas off, but plain zstd bodies are still decoded.
	Store *zdelta.Store
	// Token, when set, must arrive in TokenHeader.
	Token string
	// Transport reaches the upstreams. Nil means http.DefaultTransport.
	Transport http.RoundTripper
	Logger    *slog.Logger
}

// Handler is the relay.
type Handler struct {
	cfg    Config
	routes []Route
	proxy  *httputil.ReverseProxy
	log    *slog.Logger
	// started and inflight answer the docker-updater checks.
	started  time.Time
	inflight atomic.Int64
}

// New validates cfg and returns the relay.
func New(cfg Config) (*Handler, error) {
	if len(cfg.Routes) == 0 {
		return nil, errors.New("at least one route is required")
	}
	if cfg.MaxBodyBytes <= 0 {
		return nil, errors.New("MaxBodyBytes must be positive")
	}
	routes := append([]Route(nil), cfg.Routes...)
	// Longest prefix first, so /a/b wins over /a.
	sort.Slice(routes, func(i, j int) bool { return len(routes[i].Prefix) > len(routes[j].Prefix) })
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	for _, rt := range routes {
		if rt.Prefix == "/"+reservedRoute || strings.HasPrefix(rt.Prefix, "/"+reservedRoute+"/") {
			return nil, fmt.Errorf("route %s: the name %s is reserved", rt.Prefix, reservedRoute)
		}
	}
	h := &Handler{cfg: cfg, routes: routes, log: logger, started: time.Now().UTC()}
	h.proxy = &httputil.ReverseProxy{
		Rewrite:       func(*httputil.ProxyRequest) {},
		Transport:     cfg.Transport,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			h.log.Error("upstream request failed", "path", r.URL.Path, "err", err)
			writeError(w, http.StatusBadGateway, "upstream_unreachable", err.Error())
		},
	}
	return h, nil
}

func (h *Handler) advertise(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Accept-Encoding", zdelta.AcceptEncodingValue)
	if h.cfg.Store != nil && hasCredential(r.Header) {
		w.Header().Set(zdelta.DeltaHeader, zdelta.DeltaValue)
	}
}

func (h *Handler) match(path string) (Route, string, bool) {
	for _, rt := range h.routes {
		if path == rt.Prefix || strings.HasPrefix(path, rt.Prefix+"/") {
			return rt, strings.TrimPrefix(path, rt.Prefix), true
		}
	}
	return Route{}, "", false
}

// upload is how one request body arrived. The request log line reports it.
type upload struct {
	// kind is plain, zstd or delta.
	kind string
	// wire is the body size the client sent.
	wire  int64
	plain int64
}

// tally records what the relay sent back. Unwrap lets http.ResponseController
// reach Flush and Hijack on the real writer.
type tally struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (t *tally) Unwrap() http.ResponseWriter { return t.ResponseWriter }

func (t *tally) WriteHeader(code int) {
	// A 1xx status is not the final status.
	if t.status == 0 && code >= 200 {
		t.status = code
	}
	t.ResponseWriter.WriteHeader(code)
}

func (t *tally) Write(p []byte) (int, error) {
	if t.status == 0 {
		t.status = http.StatusOK
	}
	n, err := t.ResponseWriter.Write(p)
	t.bytes += int64(n)
	return n, err
}

// ServeHTTP relays one request and logs one line for it. The line has the path
// only: a query can hold an API key.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, updaterPrefix) {
		h.updater(w, r)
		return
	}
	h.inflight.Add(1)
	defer h.inflight.Add(-1)
	start := time.Now()
	out := &tally{ResponseWriter: w}
	up := &upload{kind: "plain", wire: r.ContentLength}
	h.relay(out, r, up)

	attrs := []any{"method", r.Method, "path", r.URL.Path, "status", out.status, "upload", up.kind}
	if up.wire >= 0 {
		attrs = append(attrs, "wire_bytes", up.wire)
	}
	if up.kind != "plain" {
		attrs = append(attrs, "plain_bytes", up.plain)
	}
	attrs = append(attrs, "response_bytes", out.bytes, "duration", time.Since(start).Round(time.Millisecond))
	level := slog.LevelInfo
	if out.status >= 400 {
		level = slog.LevelWarn
	}
	h.log.Log(r.Context(), level, "request", attrs...)
}

func (h *Handler) relay(w http.ResponseWriter, r *http.Request, up *upload) {
	h.advertise(w, r)
	if h.cfg.Token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get(TokenHeader)), []byte(h.cfg.Token)) != 1 {
		writeError(w, http.StatusUnauthorized, "relay_unauthorized", "missing or wrong "+TokenHeader+"; the upstream key goes in the usual API key header")
		return
	}
	rt, rest, ok := h.match(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "relay_no_route", "no route for "+r.URL.Path)
		return
	}
	if err := h.decodeBody(w, r, storeScope(rt, r.Header), up); err != nil {
		return
	}
	r.Header.Del(zdelta.DeltaHeader)
	r.Header.Del(zdelta.BaseHeader)
	r.Header.Del(TokenHeader)

	out := r.Clone(r.Context())
	out.URL.Scheme = rt.Upstream.Scheme
	out.URL.Host = rt.Upstream.Host
	out.URL.Path = joinPath(rt.Upstream.Path, rest)
	out.URL.RawPath = ""
	out.Host = rt.Upstream.Host
	h.proxy.ServeHTTP(w, out)
}

// credentialHeaders carry the upstream API key in the dialects the relay serves.
var credentialHeaders = []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Api-Key"}

// storeScope binds a stored body to the route and the upstream credential of
// the request that sent it. A base hash alone is not enough to use the body. A
// caller with another key could otherwise send a delta that copies the body out
// to its own upstream account.
func storeScope(rt Route, header http.Header) string {
	sum := sha256.New()
	sum.Write([]byte(rt.Prefix))
	for _, name := range credentialHeaders {
		sum.Write([]byte{0})
		sum.Write([]byte(header.Get(name)))
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// hasCredential reports whether the request carries an upstream API key.
// Deltas need one: without it, every caller would share one store scope.
func hasCredential(header http.Header) bool {
	for _, name := range credentialHeaders {
		if strings.TrimSpace(header.Get(name)) != "" {
			return true
		}
	}
	return false
}

// decodeBody replaces a zstd body with the plain bytes it encodes. It writes
// the error response itself and returns non-nil when the request must stop.
func (h *Handler) decodeBody(w http.ResponseWriter, r *http.Request, scope string, up *upload) error {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	encoding := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	deltas := h.cfg.Store != nil && hasCredential(r.Header)
	keep := deltas && r.Header.Get(zdelta.DeltaHeader) != ""
	baseHash := strings.ToLower(strings.TrimSpace(r.Header.Get(zdelta.BaseHeader)))
	if encoding != "zstd" && !keep {
		return nil
	}

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.cfg.MaxBodyBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "relay_body_too_large", err.Error())
		return err
	}
	up.wire = int64(len(raw))
	plain := raw
	if encoding == "zstd" {
		up.kind = "zstd"
		var base []byte
		if baseHash != "" {
			up.kind = "delta"
			var found bool
			if deltas {
				base, found = h.cfg.Store.Get(scope, baseHash)
			}
			if !found {
				w.Header().Set(zdelta.BaseMissingHeader, "1")
				writeError(w, http.StatusConflict, "zstd_base_missing", "the relay does not hold base "+baseHash+"; send the body again without "+zdelta.BaseHeader)
				return errors.New("base missing")
			}
		}
		plain, err = zdelta.Decode(raw, base, h.cfg.MaxBodyBytes)
		if errors.Is(err, zdelta.ErrTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "relay_body_too_large", err.Error())
			return err
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "zstd_invalid", err.Error())
			return err
		}
		up.plain = int64(len(plain))
		r.Header.Del("Content-Encoding")
		h.log.Debug("decoded request body", "path", r.URL.Path, "wire_bytes", len(raw), "plain_bytes", len(plain), "delta", base != nil)
	} else if baseHash != "" {
		writeError(w, http.StatusBadRequest, "zstd_invalid", zdelta.BaseHeader+" needs Content-Encoding: zstd")
		return errors.New("base without zstd")
	}
	if keep {
		h.cfg.Store.Put(scope, plain)
	}
	r.Body = io.NopCloser(bytes.NewReader(plain))
	r.ContentLength = int64(len(plain))
	r.Header.Set("Content-Length", strconv.Itoa(len(plain)))
	return nil
}

func joinPath(base, rest string) string {
	switch {
	case rest == "":
		return base
	case base == "" || base == "/":
		return rest
	default:
		return strings.TrimSuffix(base, "/") + rest
	}
}

// writeError answers in the shape OpenAI and Anthropic clients both read:
// an "error" object with "type" and "message".
func writeError(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": kind, "message": message},
	})
}
