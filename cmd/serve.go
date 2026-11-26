// serve.go holds upstreamKeyEnv.

package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/wow-look-at-my/ai-proxy/relay"
	"github.com/wow-look-at-my/ai-proxy/zdelta"
)

var serveFlags struct {
	listen       string
	routes       []string
	maxBodyMiB   int64
	cacheMiB     int64
	token        string
	upstreamKeys []string
	verbose      bool
}

// upstreamKeyEnv names the variable that holds a route's upstream key, so the key stays out of the process list.
func upstreamKeyEnv(name string) string {
	return "AI_PROXY_UPSTREAM_KEY_" + strings.ToUpper(strings.NewReplacer("-", "_", "/", "_").Replace(name))
}

func parseUpstreamKeys(specs []string) (map[string]string, error) {
	keys := map[string]string{}
	for _, spec := range specs {
		name, key, ok := strings.Cut(spec, "=")
		name = strings.Trim(strings.TrimSpace(name), "/")
		key = strings.TrimSpace(key)
		if !ok || name == "" || key == "" {
			return nil, fmt.Errorf("--upstream-key: want name=KEY, got %q", strings.SplitN(spec, "=", 2)[0]+"=...")
		}
		keys[name] = key
	}
	return keys, nil
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the relay",
	Example: "  ai-proxy serve --listen :8317 \\\n" +
		"    --route anthropic=https://api.anthropic.com \\\n" +
		"    --route openai=https://api.openai.com",
	RunE: func(cmd *cobra.Command, _ []string) error {
		level := slog.LevelInfo
		if serveFlags.verbose {
			level = slog.LevelDebug
		}
		logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

		keys, err := parseUpstreamKeys(serveFlags.upstreamKeys)
		if err != nil {
			return err
		}
		var routes []relay.Route
		for _, spec := range serveFlags.routes {
			rt, err := relay.ParseRoute(spec)
			if err != nil {
				return err
			}
			name := strings.TrimPrefix(rt.Prefix, "/")
			rt.Key = keys[name]
			delete(keys, name)
			if rt.Key == "" {
				rt.Key = os.Getenv(upstreamKeyEnv(name))
			}
			routes = append(routes, rt)
			logger.Info("route", "prefix", rt.Prefix, "upstream", rt.Upstream.String(), "upstream_key", rt.Key != "")
		}
		for name := range keys {
			return fmt.Errorf("--upstream-key %s: no route named %s", name, name)
		}
		var store *zdelta.Store
		if serveFlags.cacheMiB > 0 {
			store = zdelta.NewStore(serveFlags.cacheMiB << 20)
		}
		handler, err := relay.New(relay.Config{
			Routes:       routes,
			MaxBodyBytes: serveFlags.maxBodyMiB << 20,
			Store:        store,
			Token:        serveFlags.token,
			Logger:       logger,
		})
		if err != nil {
			return err
		}
		if serveFlags.token == "" {
			logger.Warn("no --token: anyone who reaches this port can send requests through it with their own upstream keys")
		}
		server := &http.Server{
			Addr:              serveFlags.listen,
			Handler:           handler,
			ReadHeaderTimeout: 30 * time.Second,
		}
		// The signals are caught before the port opens, so no request can meet the default action.
		ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, os.Interrupt)
		defer stop()
		// A second signal ends the process at once.
		context.AfterFunc(ctx, stop)
		ln, err := net.Listen("tcp", serveFlags.listen)
		if err != nil {
			return fmt.Errorf("serve: %w", err)
		}
		logger.Info("listening", "addr", ln.Addr().String(), "delta_cache_mib", serveFlags.cacheMiB)
		return serveUntilDone(ctx, server, ln, handler.InFlight, logger)
	},
}

// serveUntilDone serves on ln until ctx ends. It then takes no new connection
// and returns when every request in flight has its whole response.
func serveUntilDone(ctx context.Context, server *http.Server, ln net.Listener, inFlight func() int64, logger *slog.Logger) error {
	failed := make(chan error, 1)
	go func() { failed <- server.Serve(ln) }()
	select {
	case err := <-failed:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	logger.Info("draining", "in_flight", inFlight())
	// No deadline here: the stop timeout of the container runtime is the limit.
	if err := server.Shutdown(context.Background()); err != nil {
		return fmt.Errorf("drain: %w", err)
	}
	logger.Info("drained")
	return nil
}

func init() {
	f := serveCmd.Flags()
	f.StringVar(&serveFlags.listen, "listen", ":8317", "address to listen on")
	f.StringArrayVar(&serveFlags.routes, "route", nil, "name=https://upstream/base; requests to /name/... go to https://upstream/base/... (repeatable, required)")
	f.Int64Var(&serveFlags.maxBodyMiB, "max-body-mib", 64, "largest request body, encoded or decoded, in MiB")
	f.Int64Var(&serveFlags.cacheMiB, "delta-cache-mib", 1024, "memory for bodies clients may send deltas against, in MiB; 0 turns deltas off")
	f.StringVar(&serveFlags.token, "token", os.Getenv("AI_PROXY_TOKEN"), "token clients must send as their API key or in "+relay.TokenHeader+" (default $AI_PROXY_TOKEN)")
	f.StringArrayVar(&serveFlags.upstreamKeys, "upstream-key", nil, "name=KEY; the upstream key that replaces the token when a client sends the token as its API key (repeatable, default $AI_PROXY_UPSTREAM_KEY_<NAME>)")
	f.BoolVarP(&serveFlags.verbose, "verbose", "v", false, "log every decoded body")
	_ = serveCmd.MarkFlagRequired("route")
	rootCmd.AddCommand(serveCmd)
}
