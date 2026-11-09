// Package relay forwards requests to an upstream byte for byte. It never
// parses a body: it only undoes the zstd encoding a client applied.
package relay

import (
	"bytes"
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

	"github.com/wow-look-at-my/ai-proxy/zdelta"
)

// TokenHeader carries the relay's own access token when one is set.
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
	h := &Handler{cfg: cfg, routes: routes, log: logger}
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

func (h *Handler) advertise(w http.ResponseWriter) {
	w.Header().Set("Accept-Encoding", zdelta.AcceptEncodingValue)
	if h.cfg.Store != nil {
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

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.advertise(w)
	if h.cfg.Token != "" && r.Header.Get(TokenHeader) != h.cfg.Token {
		writeError(w, http.StatusUnauthorized, "relay_unauthorized", "missing or wrong "+TokenHeader)
		return
	}
	rt, rest, ok := h.match(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "relay_no_route", "no route for "+r.URL.Path)
		return
	}
	if err := h.decodeBody(w, r); err != nil {
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

// decodeBody replaces a zstd body with the plain bytes it encodes. It writes
// the error response itself and returns non-nil when the request must stop.
func (h *Handler) decodeBody(w http.ResponseWriter, r *http.Request) error {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	encoding := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	keep := h.cfg.Store != nil && r.Header.Get(zdelta.DeltaHeader) != ""
	baseHash := strings.ToLower(strings.TrimSpace(r.Header.Get(zdelta.BaseHeader)))
	if encoding != "zstd" && !keep {
		return nil
	}

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.cfg.MaxBodyBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "relay_body_too_large", err.Error())
		return err
	}
	plain := raw
	if encoding == "zstd" {
		var base []byte
		if baseHash != "" {
			var found bool
			if h.cfg.Store != nil {
				base, found = h.cfg.Store.Get(baseHash)
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
		r.Header.Del("Content-Encoding")
		h.log.Debug("decoded request body", "path", r.URL.Path, "wire_bytes", len(raw), "plain_bytes", len(plain), "delta", base != nil)
	} else if baseHash != "" {
		writeError(w, http.StatusBadRequest, "zstd_invalid", zdelta.BaseHeader+" needs Content-Encoding: zstd")
		return errors.New("base without zstd")
	}
	if keep {
		h.cfg.Store.Put(plain)
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
