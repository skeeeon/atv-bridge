// Package config loads atv-bridge settings: defaults, then a YAML file, then
// environment variables, each overriding the one before. TVs are not
// settings; they live in the devices KV bucket.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// DefaultPath is read when no path is given. A missing file at the default
// path is fine; a missing file at a path someone asked for is an error.
const DefaultPath = "/etc/atv-bridge/config.yaml"

type NATS struct {
	URL      string `yaml:"url"`
	Creds    string `yaml:"creds"`
	JSDomain string `yaml:"js_domain"`
}

type Config struct {
	BridgeID          string        `yaml:"bridge_id"`
	NATS              NATS          `yaml:"nats"`
	HTTPAddr          string        `yaml:"http_addr"`
	SessionDir        string        `yaml:"session_dir"`
	ClientName        string        `yaml:"client_name"`
	DevicesBucket     string        `yaml:"devices_bucket"`
	StateBucket       string        `yaml:"state_bucket"`
	CommandTimeout    time.Duration `yaml:"command_timeout"`
	PairTimeout       time.Duration `yaml:"pair_timeout"`
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
}

func defaults() Config {
	return Config{
		NATS:              NATS{URL: "nats://localhost:4222"},
		HTTPAddr:          ":8080",
		SessionDir:        "/session",
		ClientName:        "Stone Age bridge",
		DevicesBucket:     "atv_devices",
		StateBucket:       "atv_state",
		CommandTimeout:    5 * time.Second,
		PairTimeout:       60 * time.Second,
		HeartbeatInterval: 30 * time.Second,
	}
}

// Load reads path (DefaultPath if empty), applies the environment through
// getenv, and validates the result. Every problem is reported at once.
func Load(path string, getenv func(string) string) (Config, error) {
	c := defaults()
	explicit := path != ""
	if !explicit {
		path = DefaultPath
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := decode(data, &c); err != nil {
			return Config{}, fmt.Errorf("%s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist) && !explicit:
		// No file: defaults and environment only.
	default:
		return Config{}, err
	}
	if err := applyEnv(&c, getenv); err != nil {
		return Config{}, err
	}
	return c, c.validate()
}

// decode rejects unknown keys, so a typo fails at startup instead of being
// ignored.
func decode(data []byte, c *Config) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func applyEnv(c *Config, getenv func(string) string) error {
	str := func(key string, dst *string) {
		if v := getenv(key); v != "" {
			*dst = v
		}
	}
	var errs []error
	dur := func(key string, dst *time.Duration) {
		if v := getenv(key); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", key, err))
				return
			}
			*dst = d
		}
	}
	str("BRIDGE_ID", &c.BridgeID)
	str("NATS_URL", &c.NATS.URL)
	str("NATS_CREDS", &c.NATS.Creds)
	str("NATS_JS_DOMAIN", &c.NATS.JSDomain)
	str("HTTP_ADDR", &c.HTTPAddr)
	str("SESSION_DIR", &c.SessionDir)
	str("CLIENT_NAME", &c.ClientName)
	str("DEVICES_BUCKET", &c.DevicesBucket)
	str("STATE_BUCKET", &c.StateBucket)
	dur("COMMAND_TIMEOUT", &c.CommandTimeout)
	dur("PAIR_TIMEOUT", &c.PairTimeout)
	dur("HEARTBEAT_INTERVAL", &c.HeartbeatInterval)
	return errors.Join(errs...)
}

// tokenRE matches a value that is one NATS subject token and one KV key
// segment: no dots, no wildcards, no spaces.
var tokenRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ValidToken reports whether s can be a bridge or device id.
func ValidToken(s string) bool { return tokenRE.MatchString(s) }

func (c Config) validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	switch {
	case c.BridgeID == "":
		add("bridge_id (BRIDGE_ID) is required")
	case !ValidToken(c.BridgeID):
		add("bridge_id %q must use only A-Z, a-z, 0-9, - and _", c.BridgeID)
	}
	if c.NATS.Creds == "" {
		add("nats.creds (NATS_CREDS) is required: the path to the account user's creds file")
	}
	for _, f := range []struct{ name, v string }{
		{"nats.url", c.NATS.URL}, {"http_addr", c.HTTPAddr}, {"session_dir", c.SessionDir},
		{"client_name", c.ClientName}, {"devices_bucket", c.DevicesBucket}, {"state_bucket", c.StateBucket},
	} {
		if strings.TrimSpace(f.v) == "" {
			add("%s must not be empty", f.name)
		}
	}
	if c.CommandTimeout <= 0 {
		add("command_timeout must be positive")
	}
	if c.PairTimeout <= 0 {
		add("pair_timeout must be positive")
	}
	if c.HeartbeatInterval < 0 {
		add("heartbeat_interval must be 0 (off) or positive")
	}
	return errors.Join(errs...)
}
