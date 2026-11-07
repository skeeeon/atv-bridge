package bridge

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// bridgePermissions is the least a bridge user needs, for bridge_id site-a
// and the default bucket names. docs/atv-bridge.md copies this list; keep the
// two in step.
var bridgePermissions = &server.Permissions{
	Publish: &server.SubjectPermission{Allow: []string{
		"atv.site-a.heartbeat",
		"$KV.atv_state.site-a.>",
		"$JS.API.INFO",
		"$JS.API.STREAM.INFO.KV_atv_devices",
		"$JS.API.STREAM.INFO.KV_atv_state",
		"$JS.API.STREAM.CREATE.KV_atv_devices",
		"$JS.API.STREAM.CREATE.KV_atv_state",
		"$JS.API.CONSUMER.CREATE.KV_atv_devices.>",
		"$JS.API.CONSUMER.CREATE.KV_atv_state.>",
		"$JS.API.CONSUMER.DELETE.KV_atv_devices.>",
		"$JS.API.CONSUMER.DELETE.KV_atv_state.>",
		// Flow control for the watch and the key listing. A test this small
		// never needs it, but a large backlog does.
		"$JS.FC.KV_atv_devices.>",
		"$JS.FC.KV_atv_state.>",
	}},
	Subscribe: &server.SubjectPermission{Allow: []string{
		"_INBOX.>",
		"atv.site-a.>",
		"$SRV.>",
	}},
	Response: &server.ResponsePermission{MaxMsgs: 1},
}

// The documented permissions are enough for everything the bridge does:
// creating its buckets, deleting orphaned state at startup, watching its
// TVs, writing and deleting state, the heartbeat, pairing and commands.
func TestLeastPrivilegePermissions(t *testing.T) {
	s := runServer(t, []*server.User{
		{Username: "admin", Password: "admin"},
		{Username: "bridge", Password: "bridge", Permissions: bridgePermissions},
	})
	admin := connect(t, s, nats.UserInfo("admin", "admin"))

	// An orphan for startup to delete: its bucket must exist first, so the
	// admin creates the state bucket and the bridge creates the devices one.
	js, _ := jetstream.New(admin)
	stateKV, err := js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: "atv_state", History: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stateKV.Put(context.Background(), "site-a.gone", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	bridgeNC := connect(t, s, nats.UserInfo("bridge", "bridge"),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			if strings.Contains(strings.ToLower(err.Error()), "permissions violation") {
				t.Errorf("permissions violation: %v", err)
			}
		}))
	e := newEnvOn(t, bridgeNC, admin)

	if _, found := e.state("gone"); found {
		t.Error("orphaned state not deleted at startup")
	}

	sub, err := admin.SubscribeSync("atv.site-a.heartbeat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sub.NextMsg(2 * time.Second); err != nil {
		t.Errorf("no heartbeat: %v", err)
	}

	e.putDevice("lounge", `{"host":"127.0.0.1"}`)
	e.waitState("lounge", "unpaired", func(s State) bool { return !s.Paired })
	e.pair("lounge")
	e.waitState("lounge", "connected", connected)
	e.mustOK("atv.site-a.lounge.key", `{"key":"HOME"}`)
	e.mustOK("atv.site-a.devices", "")
	e.mustOK("$SRV.PING.atv-bridge", "")

	if err := e.devicesKV.Delete(context.Background(), "site-a.lounge"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, found := e.state("lounge"); !found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("state not deleted with least-privilege permissions")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
