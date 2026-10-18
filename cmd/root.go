// Package cmd implements the gogalaxy command line interface.
package cmd

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/jvzantvoort/gogalaxy/internal/config"
	"github.com/jvzantvoort/gogalaxy/internal/service"
	"github.com/jvzantvoort/gogalaxy/internal/storage"

	// Register the built-in storage driver.
	_ "github.com/jvzantvoort/gogalaxy/internal/storage/file"
)

var (
	cfgFile string
	v       = viper.New()
)

var rootCmd = &cobra.Command{
	Use:   "gogalaxy",
	Short: "A single binary Ansible Galaxy server",
	Long: `gogalaxy serves Ansible collections and roles through an API compatible
with galaxy_ng, using plain files for storage.

Start the server with "gogalaxy serve"; the other sub commands manage users,
namespaces and content directly in the storage directory.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		return config.Init(v, cfgFile)
	},
}

// Execute runs the CLI.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVarP(&cfgFile, "config", "c", "", "configuration file (default: ./gogalaxy.yaml, ~/.config/gogalaxy/gogalaxy.yaml, /etc/gogalaxy/gogalaxy.yaml)")
	pf.String("data-dir", "", "storage directory (storage.path)")
	pf.String("log-level", "", "log level: debug, info, warn, error (log.level)")
	pf.String("log-format", "", "log format: text or json (log.format)")
	bindFlag(pf.Lookup("data-dir"), "storage.path")
	bindFlag(pf.Lookup("log-level"), "log.level")
	bindFlag(pf.Lookup("log-format"), "log.format")
}

// bindFlag binds a flag to a configuration key; the flag wins when set.
func bindFlag(f *pflag.Flag, key string) {
	if err := v.BindPFlag(key, f); err != nil {
		panic(err)
	}
}

// app is the runtime environment of a command.
type app struct {
	cfg   *config.Config
	store storage.Store
	svc   *service.Service
	log   *slog.Logger
}

// openApp loads the configuration and opens the storage.
func openApp() (*app, error) {
	cfg, err := config.Load(v)
	if err != nil {
		return nil, err
	}
	log, err := newLogger(cfg.Log, os.Stderr)
	if err != nil {
		return nil, err
	}
	slog.SetDefault(log)
	store, err := storage.Open(cfg.Storage)
	if err != nil {
		return nil, err
	}
	return &app{cfg: cfg, store: store, svc: service.New(cfg, store, nil, log), log: log}, nil
}

func (a *app) Close() { _ = a.store.Close() }

func newLogger(c config.LogConfig, w io.Writer) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(c.Level)); err != nil {
		return nil, fmt.Errorf("invalid log.level %q", c.Level)
	}
	opts := &slog.HandlerOptions{Level: level}
	switch strings.ToLower(c.Format) {
	case "", "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	}
	return nil, fmt.Errorf("invalid log.format %q", c.Format)
}

// splitFQCN splits "namespace.name".
func splitFQCN(s string) (string, string, error) {
	ns, name, ok := strings.Cut(s, ".")
	if !ok || ns == "" || name == "" || strings.Contains(name, ".") {
		return "", "", fmt.Errorf("expected <namespace>.<name>, got %q", s)
	}
	return ns, name, nil
}
