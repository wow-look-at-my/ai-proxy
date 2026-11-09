package cmd

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/wow-look-at-my/ai-proxy/relay"
	"github.com/wow-look-at-my/ai-proxy/zdelta"
)

var serveFlags struct {
	listen     string
	routes     []string
	maxBodyMiB int64
	cacheMiB   int64
	token      string
	verbose    bool
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

		var routes []relay.Route
		for _, spec := range serveFlags.routes {
			rt, err := relay.ParseRoute(spec)
			if err != nil {
				return err
			}
			routes = append(routes, rt)
			logger.Info("route", "prefix", rt.Prefix, "upstream", rt.Upstream.String())
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
		logger.Info("listening", "addr", serveFlags.listen, "delta_cache_mib", serveFlags.cacheMiB)
		if err := server.ListenAndServe(); err != nil {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	},
}

func init() {
	f := serveCmd.Flags()
	f.StringVar(&serveFlags.listen, "listen", ":8317", "address to listen on")
	f.StringArrayVar(&serveFlags.routes, "route", nil, "name=https://upstream/base; requests to /name/... go to https://upstream/base/... (repeatable, required)")
	f.Int64Var(&serveFlags.maxBodyMiB, "max-body-mib", 64, "largest request body, encoded or decoded, in MiB")
	f.Int64Var(&serveFlags.cacheMiB, "delta-cache-mib", 1024, "memory for bodies clients may send deltas against, in MiB; 0 turns deltas off")
	f.StringVar(&serveFlags.token, "token", os.Getenv("AI_PROXY_TOKEN"), "token clients must send in "+relay.TokenHeader+" (default $AI_PROXY_TOKEN)")
	f.BoolVarP(&serveFlags.verbose, "verbose", "v", false, "log every decoded body")
	_ = serveCmd.MarkFlagRequired("route")
	rootCmd.AddCommand(serveCmd)
}
