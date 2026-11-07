package remote

import (
	"testing"

	pb "github.com/stone-age-io/atv-bridge/third_party/atvremote/proto"
)

func TestParseKeyCode(t *testing.T) {
	tests := []struct {
		in      string
		want    pb.RemoteKeyCode
		wantErr bool
	}{
		{"KEYCODE_POWER", pb.RemoteKeyCode_KEYCODE_POWER, false},
		{"POWER", pb.RemoteKeyCode_KEYCODE_POWER, false},
		{"DPAD_UP", pb.RemoteKeyCode_KEYCODE_DPAD_UP, false},
		{"F24", pb.RemoteKeyCode_KEYCODE_F24, false},
		{"SCREENSHOT", pb.RemoteKeyCode_KEYCODE_SCREENSHOT, false},
		{"NOT_A_KEY", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseKeyCode(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseKeyCode(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("ParseKeyCode(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParseDirection(t *testing.T) {
	if _, err := ParseDirection("SIDEWAYS"); err == nil {
		t.Error("expected error for unknown direction")
	}
	got, err := ParseDirection("START_LONG")
	if err != nil || got != pb.RemoteDirection_START_LONG {
		t.Errorf("ParseDirection(START_LONG) = %v, %v", got, err)
	}
}

func TestParseKeys(t *testing.T) {
	got, err := ParseKeys("a;up;bc;KEYCODE_TV_INPUT_HDMI_1;VOLP;7")
	if err != nil {
		t.Fatal(err)
	}
	want := []pb.RemoteKeyCode{
		pb.RemoteKeyCode_KEYCODE_A,
		pb.RemoteKeyCode_KEYCODE_DPAD_UP, // shortcuts are case insensitive
		pb.RemoteKeyCode_KEYCODE_B,
		pb.RemoteKeyCode_KEYCODE_C,
		pb.RemoteKeyCode_KEYCODE_TV_INPUT_HDMI_1,
		pb.RemoteKeyCode_KEYCODE_VOLUME_UP,
		pb.RemoteKeyCode_KEYCODE_NUMPAD_7,
	}
	if len(got) != len(want) {
		t.Fatalf("ParseKeys returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("key %d: got %v, want %v", i, got[i], want[i])
		}
	}

	if _, err := ParseKeys("KEYCODE_VOLUME_UPP"); err == nil {
		t.Error("expected error for unknown key code")
	}
	if _, err := ParseKeys("a!b"); err == nil {
		t.Error("expected error for unknown character")
	}
	if keys, err := ParseKeys(""); err != nil || len(keys) != 0 {
		t.Errorf("ParseKeys(\"\") = %v, %v; want empty, nil", keys, err)
	}
}
