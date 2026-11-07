package bridge

import (
	"os"
	"regexp"
	"strings"
	"testing"

	pb "github.com/skeeeon/atv-bridge/third_party/atvremote/proto"
	"github.com/skeeeon/atv-bridge/third_party/atvremote/remote"
)

// docs/keys.md lists every key the protocol defines and names common ones in
// tables. This keeps it in step with the vendored enum: a library update that
// adds keys fails here until the list is regenerated.
func TestKeysDoc(t *testing.T) {
	data, err := os.ReadFile("../docs/keys.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	_, everyKey, ok := strings.Cut(doc, "## 4. Every Key")
	if !ok {
		t.Fatal(`docs/keys.md has no "## 4. Every Key" section`)
	}

	listed := map[string]bool{}
	for _, m := range regexp.MustCompile("`([A-Z0-9_]+)`").FindAllStringSubmatch(everyKey, -1) {
		if m[1] != "KEYCODE_" { // the intro mentions the prefix itself
			listed[m[1]] = true
		}
	}
	for name := range pb.RemoteKeyCode_value {
		short := strings.TrimPrefix(name, "KEYCODE_")
		if short != "UNKNOWN" && !listed[short] {
			t.Errorf("%s is missing from section 4 of docs/keys.md", short)
		}
	}
	for short := range listed {
		if _, err := remote.ParseKeyCode(short); err != nil {
			t.Errorf("section 4 lists %s, which is not a key", short)
		}
	}

	// Every key named in a table row elsewhere is a real key.
	tableKey := regexp.MustCompile("`([A-Z0-9_]+)`")
	before, _, _ := strings.Cut(doc, "## 3. Launching Apps")
	for _, line := range strings.Split(before, "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		for _, m := range tableKey.FindAllStringSubmatch(line, -1) {
			if _, err := remote.ParseKeyCode(m[1]); err != nil {
				t.Errorf("docs/keys.md names %s in a table, which is not a key", m[1])
			}
		}
	}
}
