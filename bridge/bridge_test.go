package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/stone-age-io/atv-bridge/internal/faketv"
	"github.com/stone-age-io/atv-bridge/internal/health"
	pb "github.com/stone-age-io/atv-bridge/third_party/atvremote/proto"
)

type env struct {
	t         *testing.T
	nc        *nats.Conn // an admin connection, standing in for the console
	devicesKV jetstream.KeyValue
	stateKV   jetstream.KeyValue
	tv        *faketv.TV
	b         *Bridge
}

func runServer(t *testing.T, users []*server.User) *server.Server {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	opts.Users = users
	s := natstest.RunServer(&opts)
	t.Cleanup(s.Shutdown)
	return s
}

func connect(t *testing.T, s *server.Server, opts ...nats.Option) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(s.ClientURL(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func testOptions(tv *faketv.TV, sessionDir string) Options {
	api, pair := tv.Ports()
	return Options{
		BridgeID:          "site-a",
		ClientName:        "test-bridge",
		SessionDir:        sessionDir,
		DevicesBucket:     "atv_devices",
		StateBucket:       "atv_state",
		CommandTimeout:    2 * time.Second,
		PairTimeout:       2 * time.Second,
		HeartbeatInterval: 50 * time.Millisecond,
		RetryMin:          20 * time.Millisecond,
		RetryMax:          100 * time.Millisecond,
		PairRetry:         100 * time.Millisecond,
		APIPort:           api,
		PairPort:          pair,
	}
}

// newEnv starts a NATS server, a fake TV and a bridge with its service.
func newEnv(t *testing.T) *env {
	t.Helper()
	s := runServer(t, nil)
	return newEnvOn(t, connect(t, s), connect(t, s))
}

// newEnvOn starts the bridge on bridgeNC; admin binds the buckets for the
// test's own reads and writes.
func newEnvOn(t *testing.T, bridgeNC, admin *nats.Conn) *env {
	t.Helper()
	e := &env{t: t, nc: admin, tv: faketv.New(t)}
	e.b = New(testOptions(e.tv, t.TempDir()), slog.New(slog.NewTextHandler(testWriter{t}, nil)), NewMetrics(prometheus.NewRegistry()))
	if err := e.b.Start(context.Background(), bridgeNC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.b.Stop)
	svc, err := e.b.AddService("0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	e.bindBuckets()
	return e
}

func (e *env) bindBuckets() {
	js, _ := jetstream.New(e.nc)
	var err error
	if e.devicesKV, err = js.KeyValue(context.Background(), "atv_devices"); err != nil {
		e.t.Fatal(err)
	}
	if e.stateKV, err = js.KeyValue(context.Background(), "atv_state"); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) putDevice(id, value string) {
	e.t.Helper()
	if _, err := e.devicesKV.Put(context.Background(), "site-a."+id, []byte(value)); err != nil {
		e.t.Fatal(err)
	}
}

// state reads a TV's state key; ok is false when the key doesn't exist.
func (e *env) state(id string) (State, bool) {
	entry, err := e.stateKV.Get(context.Background(), "site-a."+id)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return State{}, false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	var s State
	if err := json.Unmarshal(entry.Value(), &s); err != nil {
		e.t.Fatal(err)
	}
	return s, true
}

// waitState waits until the TV's state in KV satisfies ok.
func (e *env) waitState(id, what string, ok func(State) bool) State {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if s, found := e.state(id); found && ok(s) {
			return s
		}
		if time.Now().After(deadline) {
			s, _ := e.state(id)
			e.t.Fatalf("%s: want %s, have %+v", id, what, s)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func connected(s State) bool { return s.Available && s.Paired }

// request sends a request and returns the reply body and error code ("" on
// success).
func (e *env) request(subject, body string) (map[string]any, string) {
	e.t.Helper()
	msg, err := e.nc.Request(subject, []byte(body), 5*time.Second)
	if err != nil {
		e.t.Fatalf("%s: %v", subject, err)
	}
	if code := msg.Header.Get("Nats-Service-Error-Code"); code != "" {
		return map[string]any{"error": msg.Header.Get("Nats-Service-Error")}, code
	}
	var out map[string]any
	if err := json.Unmarshal(msg.Data, &out); err != nil {
		e.t.Fatalf("%s: reply %q: %v", subject, msg.Data, err)
	}
	return out, ""
}

func (e *env) mustOK(subject, body string) map[string]any {
	e.t.Helper()
	out, code := e.request(subject, body)
	if code != "" {
		e.t.Fatalf("%s %s: error %s: %v", subject, body, code, out["error"])
	}
	return out
}

func (e *env) wantCode(subject, body, want string) {
	e.t.Helper()
	out, code := e.request(subject, body)
	if code != want {
		e.t.Fatalf("%s %s: want error %s, have %q (%v)", subject, body, want, code, out)
	}
}

// received waits for the next command the fake TV gets.
func (e *env) received() *pb.RemoteMessage {
	e.t.Helper()
	select {
	case m := <-e.tv.Received:
		return m
	case <-time.After(3 * time.Second):
		e.t.Fatal("the TV received nothing")
		return nil
	}
}

// acceptAll skips pairing: the TV accepts any certificate, and the bridge
// gets one without pairing.
func (e *env) acceptAll() {
	e.t.Helper()
	e.tv.AcceptAll()
	if _, err := e.b.newRemote("test", "127.0.0.1").GenerateCertIfMissing(); err != nil {
		e.t.Fatal(err)
	}
}

// pair runs the whole pairing exchange over NATS.
func (e *env) pair(id string) {
	e.t.Helper()
	e.mustOK("atv.site-a."+id+".pair.start", "")
	e.mustOK("atv.site-a."+id+".pair.finish", `{"pin":"`+e.tv.Code()+`"}`)
}

func TestPairThenCommands(t *testing.T) {
	e := newEnv(t)
	e.putDevice("lounge", `{"host":"127.0.0.1","name":"Lounge"}`)
	e.waitState("lounge", "unpaired", func(s State) bool { return !s.Paired && s.Name == "Lounge" })
	e.wantCode("atv.site-a.lounge.key", `{"key":"HOME"}`, "409")

	e.mustOK("atv.site-a.lounge.pair.start", "")
	code := e.tv.Code()
	wrong := "00" + code[2:]
	if strings.HasPrefix(code, "00") {
		wrong = "11" + code[2:]
	}
	e.wantCode("atv.site-a.lounge.pair.finish", `{"pin":"`+wrong+`"}`, "400")
	// A wrong code keeps the pairing open; the right one still works.
	e.mustOK("atv.site-a.lounge.pair.finish", `{"pin":"`+strings.ToLower(code)+`"}`)

	s := e.waitState("lounge", "connected", connected)
	if !s.On || s.Device == nil || s.Device.Model != "FakeTV" {
		t.Errorf("state after connect: %+v", s)
	}

	e.mustOK("atv.site-a.lounge.key", `{"key":"HOME"}`)
	if m := e.received().GetRemoteKeyInject(); m.GetKeyCode() != pb.RemoteKeyCode_KEYCODE_HOME || m.GetDirection() != pb.RemoteDirection_SHORT {
		t.Errorf("key: %v", m)
	}
	e.mustOK("atv.site-a.lounge.key", `{"key":"KEYCODE_DPAD_UP","direction":"START_LONG"}`)
	if m := e.received().GetRemoteKeyInject(); m.GetKeyCode() != pb.RemoteKeyCode_KEYCODE_DPAD_UP || m.GetDirection() != pb.RemoteDirection_START_LONG {
		t.Errorf("long key: %v", m)
	}
	// Names are case-insensitive, with or without the prefix.
	e.mustOK("atv.site-a.lounge.key", `{"key":" keycode_volume_mute ","direction":"end_long"}`)
	if m := e.received().GetRemoteKeyInject(); m.GetKeyCode() != pb.RemoteKeyCode_KEYCODE_VOLUME_MUTE || m.GetDirection() != pb.RemoteDirection_END_LONG {
		t.Errorf("lowercase key: %v", m)
	}
	e.mustOK("atv.site-a.lounge.key", `{"key":"Media_Play_Pause"}`)
	if m := e.received().GetRemoteKeyInject(); m.GetKeyCode() != pb.RemoteKeyCode_KEYCODE_MEDIA_PLAY_PAUSE {
		t.Errorf("mixed-case key: %v", m)
	}
	for _, body := range []string{
		`{"key":"NOPE"}`,
		`{}`,
		`{"key":"unknown"}`,
		`{"key":"HOME","direction":"SIDEWAYS"}`,
		`{"key":"HOME","direction":"unknown_direction"}`,
	} {
		e.wantCode("atv.site-a.lounge.key", body, "400")
	}

	e.mustOK("atv.site-a.lounge.text", `{"text":"stranger things"}`)
	if edit := e.received().GetRemoteImeBatchEdit(); edit == nil || edit.GetEditInfo()[0].GetTextFieldStatus().GetValue() != "stranger things" {
		t.Errorf("text: %v", edit)
	}
	e.wantCode("atv.site-a.lounge.text", `{}`, "400")

	e.mustOK("atv.site-a.lounge.launch", `{"app":"https://www.netflix.com/title/1"}`)
	if link := e.received().GetRemoteAppLinkLaunchRequest().GetAppLink(); link != "https://www.netflix.com/title/1" {
		t.Errorf("launch: %q", link)
	}

	// The TV is on, so asking for on sends nothing.
	if out := e.mustOK("atv.site-a.lounge.power", `{"on":true}`); out["changed"] != false {
		t.Errorf("power on while on: %v", out)
	}
	if out := e.mustOK("atv.site-a.lounge.power", `{"on":false}`); out["changed"] != true {
		t.Errorf("power off while on: %v", out)
	}
	if m := e.received().GetRemoteKeyInject(); m.GetKeyCode() != pb.RemoteKeyCode_KEYCODE_POWER {
		t.Errorf("power: %v", m)
	}
	e.wantCode("atv.site-a.lounge.power", `{}`, "400")

	// What the TV reports reaches KV.
	e.tv.SetOn(false)
	e.tv.SetApp("com.netflix.ninja")
	e.tv.SetVolume(12, 100, false)
	e.waitState("lounge", "off, netflix, volume 12", func(s State) bool {
		return !s.On && s.App == "com.netflix.ninja" && s.Volume != nil && s.Volume.Level == 12
	})

	if out := e.mustOK("atv.site-a.lounge.state", ""); out["app"] != "com.netflix.ninja" {
		t.Errorf("state endpoint: %v", out)
	}
	devices := e.mustOK("atv.site-a.devices", "")["devices"].([]any)
	if len(devices) != 1 || devices[0].(map[string]any)["status"] != "connected" {
		t.Errorf("devices: %v", devices)
	}
	e.wantCode("atv.site-a.attic.key", `{"key":"HOME"}`, "404")
}

func TestOnePairingAtATime(t *testing.T) {
	e := newEnv(t)
	e.putDevice("a", `{"host":"127.0.0.1"}`)
	e.putDevice("b", `{"host":"127.0.0.1"}`)
	e.waitState("b", "unpaired", func(s State) bool { return !s.Paired })

	e.wantCode("atv.site-a.a.pair.finish", `{"pin":"ABCDEF"}`, "409") // nothing started
	e.mustOK("atv.site-a.a.pair.start", "")
	e.tv.Code()
	e.wantCode("atv.site-a.b.pair.start", "", "409")
	// Starting again for the same TV replaces the pairing.
	e.mustOK("atv.site-a.a.pair.start", "")
	e.tv.Code()

	// The pairing times out, then b can pair.
	time.Sleep(e.b.opts.PairTimeout + 200*time.Millisecond)
	e.wantCode("atv.site-a.a.pair.finish", `{"pin":"ABCDEF"}`, "409")
	e.pair("b")
	e.waitState("b", "connected", connected)
}

func TestDeviceListChanges(t *testing.T) {
	e := newEnv(t)
	e.acceptAll()
	e.putDevice("lounge", `{"host":"127.0.0.1"}`)
	e.waitState("lounge", "connected", connected)

	// A bad record shows its error in state, and commands are refused.
	e.putDevice("lounge", `{"hostname":"127.0.0.1"}`)
	s := e.waitState("lounge", "an error", func(s State) bool { return s.Error != "" })
	if !strings.Contains(s.Error, "hostname") {
		t.Errorf("error should name the bad field: %q", s.Error)
	}
	e.wantCode("atv.site-a.lounge.key", `{"key":"HOME"}`, "409")

	// Fixing it connects again; "localhost" is a new host for the same TV.
	e.putDevice("lounge", `{"host":"localhost"}`)
	e.waitState("lounge", "connected at localhost", func(s State) bool { return connected(s) && s.Host == "localhost" })

	// Deleting the record deletes the state.
	if err := e.devicesKV.Delete(context.Background(), "site-a.lounge"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, found := e.state("lounge"); !found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("state key not deleted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.wantCode("atv.site-a.lounge.key", `{"key":"HOME"}`, "404")

	// Other bridges' keys are none of this bridge's business.
	if _, err := e.devicesKV.Put(context.Background(), "site-b.kitchen", []byte(`{"host":"127.0.0.1"}`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(e.mustOK("atv.site-a.devices", "")["devices"].([]any)); n != 0 {
		t.Errorf("site-a lists %d devices, want 0", n)
	}
}

func TestTVForgetsBridge(t *testing.T) {
	e := newEnv(t)
	e.putDevice("lounge", `{"host":"127.0.0.1"}`)
	e.waitState("lounge", "unpaired", func(s State) bool { return !s.Paired })
	e.pair("lounge")
	e.waitState("lounge", "connected", connected)

	// A factory reset: the library's reconnect gets InvalidAuth.
	e.tv.Forget()
	e.waitState("lounge", "unpaired", func(s State) bool { return !s.Paired && !s.Available })

	// Pairing again reconnects.
	e.pair("lounge")
	e.waitState("lounge", "connected", connected)
}

func TestTVDropsAndComesBack(t *testing.T) {
	e := newEnv(t)
	e.acceptAll()
	e.putDevice("lounge", `{"host":"127.0.0.1"}`)
	e.waitState("lounge", "connected", connected)

	e.tv.Drop()
	// The library reconnects on its own.
	deadline := time.Now().Add(5 * time.Second)
	for e.tv.Connections() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no reconnect after a drop")
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.waitState("lounge", "connected", connected)
	e.mustOK("atv.site-a.lounge.key", `{"key":"HOME"}`)

	// The TV goes away for good: still paired, but unreachable.
	e.tv.Close()
	e.waitState("lounge", "unreachable", func(s State) bool { return s.Paired && !s.Available })
	e.wantCode("atv.site-a.lounge.key", `{"key":"HOME"}`, "503")
}

func TestRewriteAllRestoresState(t *testing.T) {
	e := newEnv(t)
	e.acceptAll()
	e.putDevice("lounge", `{"host":"127.0.0.1"}`)
	e.waitState("lounge", "connected", connected)

	// Stands in for puts lost while NATS was down.
	if err := e.stateKV.Delete(context.Background(), "site-a.lounge"); err != nil {
		t.Fatal(err)
	}
	e.b.RewriteAll()
	e.waitState("lounge", "connected", connected)
}

func TestOrphanStateDeletedAtStart(t *testing.T) {
	s := runServer(t, nil)
	admin := connect(t, s)
	js, _ := jetstream.New(admin)
	stateKV, err := js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: "atv_state", History: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"site-a.gone", "site-b.other"} {
		if _, err := stateKV.Put(context.Background(), k, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	newEnvOn(t, connect(t, s), admin)
	if _, err := stateKV.Get(context.Background(), "site-a.gone"); !errors.Is(err, jetstream.ErrKeyNotFound) {
		t.Errorf("site-a.gone: want deleted, have %v", err)
	}
	if _, err := stateKV.Get(context.Background(), "site-b.other"); err != nil {
		t.Errorf("another bridge's key must stay: %v", err)
	}
}

func TestHeartbeat(t *testing.T) {
	e := newEnv(t)
	e.acceptAll()
	sub, err := e.nc.SubscribeSync("atv.site-a.heartbeat")
	if err != nil {
		t.Fatal(err)
	}
	e.putDevice("lounge", `{"host":"127.0.0.1"}`)
	e.waitState("lounge", "connected", connected)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		msg, err := sub.NextMsg(time.Second)
		if err != nil {
			t.Fatal(err)
		}
		var hb heartbeat
		if err := json.Unmarshal(msg.Data, &hb); err != nil {
			t.Fatal(err)
		}
		if hb.Bridge == "site-a" && hb.Devices == 1 && hb.Connected == 1 {
			return
		}
	}
	t.Fatal("no heartbeat with 1 connected device")
}

func TestReady(t *testing.T) {
	e := newEnv(t)
	byName := func() map[string]health.Result {
		out := map[string]health.Result{}
		for _, r := range e.b.Ready() {
			out[r.Name] = r
		}
		return out
	}
	r := byName()
	if r["nats"].State != health.OK || r["buckets"].State != health.OK || r["certificate"].State != health.Skipped {
		t.Errorf("fresh bridge: %+v", r)
	}
	e.putDevice("lounge", `{"host":"127.0.0.1"}`)
	e.putDevice("bad", `not json`)
	e.waitState("bad", "an error", func(s State) bool { return s.Error != "" })
	e.waitState("lounge", "unpaired", func(s State) bool { return !s.Paired })
	r = byName()
	if r["certificate"].State != health.Warn || r["devices"].State != health.Warn || !strings.Contains(r["devices"].Detail, "bad") {
		t.Errorf("TVs but no certificate, one bad record: %+v", r)
	}
	e.pair("lounge")
	if r = byName(); r["certificate"].State != health.OK {
		t.Errorf("after pairing: %+v", r["certificate"])
	}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

// A platform-generated code works as a bridge id and a device id: subjects
// and KV keys keep its case.
func TestUppercaseIDs(t *testing.T) {
	s := runServer(t, nil)
	admin := connect(t, s)
	e := &env{t: t, nc: admin, tv: faketv.New(t)}
	opts := testOptions(e.tv, t.TempDir())
	opts.BridgeID = "ATV-V7Q-NB9"
	e.b = New(opts, slog.New(slog.NewTextHandler(testWriter{t}, nil)), NewMetrics(prometheus.NewRegistry()))
	if err := e.b.Start(context.Background(), connect(t, s)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.b.Stop)
	svc, err := e.b.AddService("0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	e.bindBuckets()
	e.acceptAll()

	if _, err := e.devicesKV.Put(context.Background(), "ATV-V7Q-NB9.TV-K2P", []byte(`{"host":"127.0.0.1"}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if entry, err := e.stateKV.Get(context.Background(), "ATV-V7Q-NB9.TV-K2P"); err == nil {
			var st State
			_ = json.Unmarshal(entry.Value(), &st)
			if connected(st) {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("TV-K2P never connected")
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.mustOK("atv.ATV-V7Q-NB9.TV-K2P.key", `{"key":"HOME"}`)
	if m := e.received().GetRemoteKeyInject(); m.GetKeyCode() != pb.RemoteKeyCode_KEYCODE_HOME {
		t.Errorf("key: %v", m)
	}
	// Subjects are case-sensitive: the lowercase bridge is another bridge.
	if _, err := e.nc.Request("atv.atv-v7q-nb9.TV-K2P.key", []byte(`{"key":"HOME"}`), time.Second); !errors.Is(err, nats.ErrNoResponders) {
		t.Errorf("lowercase subject: want no responders, have %v", err)
	}
}
