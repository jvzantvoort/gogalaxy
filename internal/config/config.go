// Package config defines the gogalaxy configuration and loads it through viper.
//
// Values are resolved (highest priority first) from command line flags,
// GOGALAXY_* environment variables, the configuration file and the defaults
// below. Nested keys map to environment variables by replacing dots with
// underscores, e.g. storage.path -> GOGALAXY_STORAGE_PATH.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// EnvPrefix is the prefix of environment variables read by gogalaxy.
const EnvPrefix = "GOGALAXY"

// Config is the complete runtime configuration.
type Config struct {
	Server  ServerConfig  `mapstructure:"server"`
	API     APIConfig     `mapstructure:"api"`
	Storage StorageConfig `mapstructure:"storage"`
	Content ContentConfig `mapstructure:"content"`
	Auth    AuthConfig    `mapstructure:"auth"`
	Log     LogConfig     `mapstructure:"log"`
}

// ServerConfig controls the HTTP listener.
type ServerConfig struct {
	Listen string `mapstructure:"listen"`
	// PublicURL is the externally visible base URL (scheme://host[:port]) used
	// for absolute links such as download URLs. When empty it is derived from
	// the request, honouring X-Forwarded-Proto and X-Forwarded-Host.
	PublicURL       string        `mapstructure:"public_url"`
	TLSCert         string        `mapstructure:"tls_cert"`
	TLSKey          string        `mapstructure:"tls_key"`
	ReadTimeout     time.Duration `mapstructure:"read_timeout"`
	WriteTimeout    time.Duration `mapstructure:"write_timeout"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
	// SecretKey signs short-lived role download URLs. A random key is
	// generated at start-up when empty.
	SecretKey string `mapstructure:"secret_key"`
}

// APIConfig controls the shape of the galaxy API.
type APIConfig struct {
	// Prefix is the API root path, galaxy_ng uses /api/galaxy.
	Prefix string `mapstructure:"prefix"`
	// ReportedVersion is the galaxy_ng version advertised to clients.
	ReportedVersion string `mapstructure:"reported_version"`
}

// StorageConfig selects and configures the storage driver.
type StorageConfig struct {
	Driver string `mapstructure:"driver"`
	Path   string `mapstructure:"path"`
	// Index keeps an in-memory collection index in the server, for drivers
	// that support change tracking.
	Index bool `mapstructure:"index"`
	// Options carries driver specific settings.
	Options map[string]any `mapstructure:"options"`
}

// ContentConfig controls content handling.
type ContentConfig struct {
	// Repositories lists the collection repositories (distributions) served.
	Repositories []string `mapstructure:"repositories"`
	// DefaultRepository is served under <prefix>/v3/.
	DefaultRepository string `mapstructure:"default_repository"`
	// UploadRepository receives uploads made outside a content/<repo>/ path.
	UploadRepository string `mapstructure:"upload_repository"`
	// AutoCreateNamespaces creates missing namespaces on upload, owned by the uploader.
	AutoCreateNamespaces bool `mapstructure:"auto_create_namespaces"`
	// MaxUploadSize is the maximum accepted artifact size in bytes.
	MaxUploadSize int64 `mapstructure:"max_upload_size"`
	// EnableLegacyRoles enables the v1 (roles) API.
	EnableLegacyRoles bool `mapstructure:"enable_legacy_roles"`
}

// AuthConfig controls authentication.
type AuthConfig struct {
	// Backends lists the authenticators tried in order.
	Backends []string `mapstructure:"backends"`
	// AnonymousRead allows unauthenticated browsing and downloading.
	AnonymousRead bool `mapstructure:"anonymous_read"`
	// Options carries backend specific settings, keyed by backend name.
	Options map[string]map[string]any `mapstructure:"options"`
}

// LogConfig controls logging.
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// SetDefaults registers the default values on v.
func SetDefaults(v *viper.Viper) {
	v.SetDefault("server.listen", ":8000")
	v.SetDefault("server.public_url", "")
	v.SetDefault("server.tls_cert", "")
	v.SetDefault("server.tls_key", "")
	v.SetDefault("server.read_timeout", "5m")
	v.SetDefault("server.write_timeout", "5m")
	v.SetDefault("server.shutdown_timeout", "15s")
	v.SetDefault("server.secret_key", "")

	v.SetDefault("api.prefix", "/api/galaxy")
	v.SetDefault("api.reported_version", "")

	v.SetDefault("storage.driver", "file")
	v.SetDefault("storage.path", "./data")
	v.SetDefault("storage.index", true)

	v.SetDefault("content.repositories", []string{"published"})
	v.SetDefault("content.default_repository", "published")
	v.SetDefault("content.upload_repository", "published")
	v.SetDefault("content.auto_create_namespaces", false)
	v.SetDefault("content.max_upload_size", 100*1024*1024)
	v.SetDefault("content.enable_legacy_roles", true)

	v.SetDefault("auth.backends", []string{"token", "basic"})
	v.SetDefault("auth.anonymous_read", true)

	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "text")
}

// Init prepares v: defaults, environment binding and the configuration file.
// When file is empty the standard locations are searched and a missing file
// is not an error.
func Init(v *viper.Viper, file string) error {
	SetDefaults(v)
	v.SetEnvPrefix(EnvPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if file != "" {
		v.SetConfigFile(file)
	} else {
		v.SetConfigName("gogalaxy")
		v.AddConfigPath(".")
		if dir, err := os.UserConfigDir(); err == nil {
			v.AddConfigPath(filepath.Join(dir, "gogalaxy"))
		}
		v.AddConfigPath("/etc/gogalaxy")
	}
	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if file != "" || !errors.As(err, &notFound) {
			return fmt.Errorf("reading configuration: %w", err)
		}
	}
	return nil
}

// Load decodes v into a validated Config.
func Load(v *viper.Viper) (*Config, error) {
	var c Config
	if err := v.Unmarshal(&c); err != nil {
		return nil, fmt.Errorf("decoding configuration: %w", err)
	}
	c.normalize()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) normalize() {
	c.API.Prefix = "/" + strings.Trim(c.API.Prefix, "/")
	c.Server.PublicURL = strings.TrimRight(c.Server.PublicURL, "/")
	c.Auth.Backends = splitList(c.Auth.Backends)
	c.Content.Repositories = splitList(c.Content.Repositories)
}

// splitList accepts both proper lists and a single comma separated string,
// which is what environment variables produce.
func splitList(in []string) []string {
	var out []string
	for _, s := range in {
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// Validate checks the configuration for consistency.
func (c *Config) Validate() error {
	if c.API.Prefix == "/" {
		return fmt.Errorf("api.prefix must not be empty")
	}
	if len(c.Content.Repositories) == 0 {
		return fmt.Errorf("content.repositories must list at least one repository")
	}
	for _, r := range []string{c.Content.DefaultRepository, c.Content.UploadRepository} {
		if !c.HasRepository(r) {
			return fmt.Errorf("repository %q is not listed in content.repositories", r)
		}
	}
	if (c.Server.TLSCert == "") != (c.Server.TLSKey == "") {
		return fmt.Errorf("server.tls_cert and server.tls_key must be set together")
	}
	return nil
}

// HasRepository reports whether name is a configured repository.
func (c *Config) HasRepository(name string) bool {
	for _, r := range c.Content.Repositories {
		if r == name {
			return true
		}
	}
	return false
}

// ReportedVersion returns the galaxy_ng version advertised to clients.
func (c *Config) ReportedVersion(fallback string) string {
	if c.API.ReportedVersion != "" {
		return c.API.ReportedVersion
	}
	return fallback
}
