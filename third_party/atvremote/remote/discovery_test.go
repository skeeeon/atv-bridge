package remote

import "testing"

func TestInstanceName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{`Living\ Room\ TV._androidtvremote2._tcp.local.`, "Living Room TV"},
		{`LuLu\'s._androidtvremote2._tcp.local.`, "LuLu's"},
		{`TV\.2._androidtvremote2._tcp.local.`, "TV.2"},
		{`caf\195\169._androidtvremote2._tcp.local.`, "café"},
		{`plain._androidtvremote2._tcp.local.`, "plain"},
	}
	for _, tt := range tests {
		if got := instanceName(tt.in); got != tt.want {
			t.Errorf("instanceName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
