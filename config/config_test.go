package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFileThenEnv(t *testing.T) {
	path := writeFile(t, `
bridge_id: site-a
nats:
  url: nats://leaf:4222
  creds: /creds/user.creds
command_timeout: 3s
`)
	c, err := Load(path, envOf(map[string]string{"NATS_URL": "tls://hub:4222", "PAIR_TIMEOUT": "90s"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.BridgeID != "site-a" || c.NATS.Creds != "/creds/user.creds" {
		t.Errorf("file values not read: %+v", c)
	}
	if c.NATS.URL != "tls://hub:4222" {
		t.Errorf("env should override the file: url = %q", c.NATS.URL)
	}
	if c.CommandTimeout != 3*time.Second || c.PairTimeout != 90*time.Second {
		t.Errorf("durations: command %s, pair %s", c.CommandTimeout, c.PairTimeout)
	}
	if c.StateBucket != "atv_state" || c.HeartbeatInterval != 30*time.Second {
		t.Errorf("defaults not kept: %+v", c)
	}
}

func TestEnvOnlyWithDefaultPathMissing(t *testing.T) {
	// DefaultPath doesn't exist on a test machine; that's fine.
	c, err := Load("", envOf(map[string]string{"BRIDGE_ID": "lab", "NATS_CREDS": "/x.creds"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.BridgeID != "lab" {
		t.Errorf("bridge_id = %q", c.BridgeID)
	}
}

func TestExplicitPathMustExist(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml"), envOf(nil)); err == nil {
		t.Fatal("want an error for a missing -config file")
	}
}

func TestUnknownKeyFails(t *testing.T) {
	path := writeFile(t, "bridge_id: a\nnats:\n  creds: x\nheartbeat_intervall: 5s\n")
	_, err := Load(path, envOf(nil))
	if err == nil || !strings.Contains(err.Error(), "heartbeat_intervall") {
		t.Fatalf("want an unknown-key error, got %v", err)
	}
}

func TestEveryProblemReported(t *testing.T) {
	path := writeFile(t, "bridge_id: Site.A\ncommand_timeout: 0s\n")
	_, err := Load(path, envOf(map[string]string{"HEARTBEAT_INTERVAL": "soon"}))
	if err == nil || !strings.Contains(err.Error(), "HEARTBEAT_INTERVAL") {
		t.Fatalf("want the env parse error first, got %v", err)
	}
	_, err = Load(path, envOf(nil))
	if err == nil {
		t.Fatal("want validation errors")
	}
	for _, want := range []string{"bridge_id", "nats.creds", "command_timeout"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}
