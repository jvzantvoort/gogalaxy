package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/jvzantvoort/gogalaxy/internal/auth"
	"github.com/jvzantvoort/gogalaxy/internal/config"
	"github.com/jvzantvoort/gogalaxy/internal/storage"
	"github.com/jvzantvoort/gogalaxy/internal/version"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Inspect the configuration",
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the effective configuration",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(v)
		if err != nil {
			return err
		}
		if cfg.Server.SecretKey != "" {
			cfg.Server.SecretKey = "********"
		}
		if f := v.ConfigFileUsed(); f != "" {
			fmt.Printf("# configuration file: %s\n", f)
		} else {
			fmt.Println("# no configuration file found, using defaults and environment")
		}
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		return enc.Encode(toYAML(cfg))
	},
}

// toYAML converts the config to a map using its mapstructure keys.
func toYAML(c *config.Config) map[string]any {
	return map[string]any{
		"server": map[string]any{
			"listen": c.Server.Listen, "public_url": c.Server.PublicURL,
			"tls_cert": c.Server.TLSCert, "tls_key": c.Server.TLSKey,
			"read_timeout": c.Server.ReadTimeout.String(), "write_timeout": c.Server.WriteTimeout.String(),
			"shutdown_timeout": c.Server.ShutdownTimeout.String(), "secret_key": c.Server.SecretKey,
		},
		"api":     map[string]any{"prefix": c.API.Prefix, "reported_version": c.ReportedVersion(version.GalaxyNGCompat)},
		"storage": map[string]any{"driver": c.Storage.Driver, "path": c.Storage.Path, "options": c.Storage.Options},
		"content": map[string]any{
			"repositories": c.Content.Repositories, "default_repository": c.Content.DefaultRepository,
			"upload_repository": c.Content.UploadRepository, "auto_create_namespaces": c.Content.AutoCreateNamespaces,
			"max_upload_size": c.Content.MaxUploadSize, "enable_legacy_roles": c.Content.EnableLegacyRoles,
		},
		"auth": map[string]any{"backends": c.Auth.Backends, "anonymous_read": c.Auth.AnonymousRead, "options": c.Auth.Options},
		"log":  map[string]any{"level": c.Log.Level, "format": c.Log.Format},
	}
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, _ []string) {
		fmt.Printf("gogalaxy %s", version.Version)
		if version.Commit != "" {
			fmt.Printf(" (%s)", version.Commit)
		}
		fmt.Printf("\ngalaxy_ng API compatibility: %s\n", version.GalaxyNGCompat)
		fmt.Printf("storage drivers: %v\nauth backends: %v\n", storage.Drivers(), auth.Backends())
	},
}

func init() {
	configCmd.AddCommand(configShowCmd)
	rootCmd.AddCommand(configCmd, versionCmd)
}
