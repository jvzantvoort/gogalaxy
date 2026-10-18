package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/jvzantvoort/gogalaxy/internal/api"
	"github.com/jvzantvoort/gogalaxy/internal/auth"
	"github.com/jvzantvoort/gogalaxy/internal/version"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the galaxy API server",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		a, err := openApp()
		if err != nil {
			return err
		}
		defer a.Close()

		chain, err := auth.Build(a.cfg.Auth.Backends, a.store, a.cfg.Auth.Options)
		if err != nil {
			return err
		}
		if a.cfg.Server.SecretKey == "" && !a.cfg.Auth.AnonymousRead {
			a.log.Warn("server.secret_key is not set; signed role download links will not survive a restart")
		}
		handler := api.NewHandler(api.Options{
			Service: a.svc,
			Auth:    chain,
			Signer:  auth.NewSigner(a.cfg.Server.SecretKey),
			Log:     a.log,
		})

		srv := &http.Server{
			Addr:         a.cfg.Server.Listen,
			Handler:      handler,
			ReadTimeout:  a.cfg.Server.ReadTimeout,
			WriteTimeout: a.cfg.Server.WriteTimeout,
		}

		ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		errc := make(chan error, 1)
		go func() {
			a.log.Info("starting gogalaxy", "version", version.Version, "listen", srv.Addr,
				"api", a.cfg.API.Prefix+"/", "storage", a.cfg.Storage.Driver+":"+a.cfg.Storage.Path,
				"repositories", a.cfg.Content.Repositories)
			if a.cfg.Server.TLSCert != "" {
				errc <- srv.ListenAndServeTLS(a.cfg.Server.TLSCert, a.cfg.Server.TLSKey)
			} else {
				errc <- srv.ListenAndServe()
			}
		}()

		select {
		case err := <-errc:
			return fmt.Errorf("server: %w", err)
		case <-ctx.Done():
		}
		a.log.Info("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), a.cfg.Server.ShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	},
}

func init() {
	f := serveCmd.Flags()
	f.String("listen", "", "listen address (server.listen, default :8000)")
	f.String("public-url", "", "external base URL used in links (server.public_url)")
	bindFlag(f.Lookup("listen"), "server.listen")
	bindFlag(f.Lookup("public-url"), "server.public_url")
	rootCmd.AddCommand(serveCmd)
}
