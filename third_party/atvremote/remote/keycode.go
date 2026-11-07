package remote

import (
	"fmt"
	"strings"

	pb "github.com/stone-age-io/atv-bridge/third_party/atvremote/proto"
)

const (
	keycodePrefix = "KEYCODE_"
	// TextPrefix in a key command string means the rest of the string is
	// passed to SendText instead of being sent as a key press.
	TextPrefix = "text:"
)

// ParseKeyCode converts a key code name from the RemoteKeyCode enum in
// remotemessage.proto (e.g. "KEYCODE_POWER" or just "POWER") to its enum
// value.
func ParseKeyCode(keyCode string) (pb.RemoteKeyCode, error) {
	name := keyCode
	if !strings.HasPrefix(name, keycodePrefix) {
		name = keycodePrefix + name
	}
	if value, ok := pb.RemoteKeyCode_value[name]; ok {
		return pb.RemoteKeyCode(value), nil
	}
	return pb.RemoteKeyCode_KEYCODE_UNKNOWN, fmt.Errorf("unknown key code: %s", keyCode)
}

// keyShortcuts are shortcuts for the most common keys, usable in ParseKeys.
var keyShortcuts = map[string]pb.RemoteKeyCode{
	"UP":    pb.RemoteKeyCode_KEYCODE_DPAD_UP,
	"DOWN":  pb.RemoteKeyCode_KEYCODE_DPAD_DOWN,
	"LEFT":  pb.RemoteKeyCode_KEYCODE_DPAD_LEFT,
	"RIGHT": pb.RemoteKeyCode_KEYCODE_DPAD_RIGHT,
	"HOME":  pb.RemoteKeyCode_KEYCODE_HOME,
	"BACK":  pb.RemoteKeyCode_KEYCODE_BACK,
	"MUTE":  pb.RemoteKeyCode_KEYCODE_MUTE,
	"VOLP":  pb.RemoteKeyCode_KEYCODE_VOLUME_UP,
	"VOLM":  pb.RemoteKeyCode_KEYCODE_VOLUME_DOWN,
	"PWR":   pb.RemoteKeyCode_KEYCODE_POWER,
	"ENTER": pb.RemoteKeyCode_KEYCODE_ENTER,
}

// ParseKeys converts a semicolon separated string of keys to a list of key
// codes. Each token can be a shortcut (UP, DOWN, LEFT, RIGHT, HOME, BACK,
// VOLP, VOLM, MUTE, PWR, ENTER — case insensitive), a KEYCODE_ name from
// remotemessage.proto or a string of alphanumeric characters and spaces that
// is converted to one key press per character. An unknown key code or
// character is an error.
//
// Example: "abc;UP;KEYCODE_TV_INPUT_HDMI_1" is parsed as [KEYCODE_A,
// KEYCODE_B, KEYCODE_C, KEYCODE_DPAD_UP, KEYCODE_TV_INPUT_HDMI_1].
func ParseKeys(data string) ([]pb.RemoteKeyCode, error) {
	var ret []pb.RemoteKeyCode
	for _, k := range strings.Split(data, ";") {
		if k == "" {
			continue
		}
		if strings.HasPrefix(k, keycodePrefix) {
			code, err := ParseKeyCode(k)
			if err != nil {
				return nil, err
			}
			ret = append(ret, code)
			continue
		}
		if code, ok := keyShortcuts[strings.ToUpper(k)]; ok {
			ret = append(ret, code)
			continue
		}
		for _, c := range strings.ToUpper(k) {
			switch {
			case c >= 'A' && c <= 'Z':
				ret = append(ret, pb.RemoteKeyCode(pb.RemoteKeyCode_value[keycodePrefix+string(c)]))
			case c >= '0' && c <= '9':
				ret = append(ret, pb.RemoteKeyCode(pb.RemoteKeyCode_value[keycodePrefix+"NUMPAD_"+string(c)]))
			case c == ' ':
				ret = append(ret, pb.RemoteKeyCode_KEYCODE_SPACE)
			default:
				return nil, fmt.Errorf("unknown character %q in key token %q", c, k)
			}
		}
	}
	return ret, nil
}

// ParseDirection converts a direction name from the RemoteDirection enum in
// remotemessage.proto ("SHORT", "START_LONG" or "END_LONG") to its enum
// value.
func ParseDirection(direction string) (pb.RemoteDirection, error) {
	if value, ok := pb.RemoteDirection_value[direction]; ok {
		return pb.RemoteDirection(value), nil
	}
	return pb.RemoteDirection_UNKNOWN_DIRECTION, fmt.Errorf("unknown direction: %s", direction)
}
