// Package bridge connects Android TVs listed in a KV bucket to NATS: it keeps
// each TV connected, writes each TV's state to a second bucket, and serves
// commands and pairing as a NATS micro service.
package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/stone-age-io/atv-bridge/internal/health"
	"github.com/stone-age-io/atv-bridge/third_party/atvremote/remote"
)

type Options struct {
	BridgeID          string
	ClientName        string
	SessionDir        string
	DevicesBucket     string
	StateBucket       string
	JSDomain          string
	CommandTimeout    time.Duration
	PairTimeout       time.Duration
	HeartbeatInterval time.Duration // 0: no heartbeat

	// Retry timing. Zero values get the production defaults; tests shorten
	// them.
	RetryMin  time.Duration // first wait after a failed connect (1s)
	RetryMax  time.Duration // longest wait between connects (30s)
	PairRetry time.Duration // wait before retrying an unpaired TV (60s)

	// TV ports. Zero means the protocol's 6466 and 6467; tests point them at
	// a fake TV.
	APIPort  int
	PairPort int
}

type Bridge struct {
	opts Options
	log  *slog.Logger
	m    *Metrics

	nc      *nats.Conn
	stateKV jetstream.KeyValue
	bound   atomic.Bool

	applyMu sync.Mutex // serialises starting and stopping devices
	mu      sync.Mutex // guards devices
	devices map[string]*device

	pairMu sync.Mutex
	pair   *pairing

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func New(opts Options, log *slog.Logger, m *Metrics) *Bridge {
	if opts.RetryMin == 0 {
		opts.RetryMin = time.Second
	}
	if opts.RetryMax == 0 {
		opts.RetryMax = 30 * time.Second
	}
	if opts.PairRetry == 0 {
		opts.PairRetry = 60 * time.Second
	}
	return &Bridge{opts: opts, log: log, m: m, devices: map[string]*device{}}
}

func (b *Bridge) certFile() string { return filepath.Join(b.opts.SessionDir, "client.crt") }
func (b *Bridge) keyFile() string  { return filepath.Join(b.opts.SessionDir, "client.key") }

func (b *Bridge) newRemote(id, host string) *remote.AndroidTVRemote {
	opts := []remote.Option{remote.WithLogger(b.log.With("device", id))}
	if b.opts.APIPort != 0 {
		opts = append(opts, remote.WithAPIPort(b.opts.APIPort))
	}
	if b.opts.PairPort != 0 {
		opts = append(opts, remote.WithPairPort(b.opts.PairPort))
	}
	return remote.New(b.opts.ClientName, b.certFile(), b.keyFile(), host, opts...)
}

// prefix is this bridge's key prefix in both buckets, and its subject
// prefix after "atv.".
func (b *Bridge) prefix() string { return b.opts.BridgeID + "." }

// Start binds the buckets, starts every TV listed for this bridge, deletes
// state left by TVs removed while the bridge was down, and keeps watching
// the devices bucket. It returns once the initial list is loaded.
func (b *Bridge) Start(ctx context.Context, nc *nats.Conn) error {
	b.nc = nc
	var js jetstream.JetStream
	var err error
	if b.opts.JSDomain != "" {
		js, err = jetstream.NewWithDomain(nc, b.opts.JSDomain)
	} else {
		js, err = jetstream.New(nc)
	}
	if err != nil {
		return err
	}
	devicesKV, err := bucket(ctx, js, b.opts.DevicesBucket, "atv-bridge TVs, one key per bridge.device")
	if err != nil {
		return err
	}
	if b.stateKV, err = bucket(ctx, js, b.opts.StateBucket, "atv-bridge TV state, one key per bridge.device"); err != nil {
		return err
	}
	b.bound.Store(true)

	runCtx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	kw, err := devicesKV.Watch(runCtx, b.prefix()+">")
	if err != nil {
		cancel()
		return fmt.Errorf("watch %s: %w", b.opts.DevicesBucket, err)
	}
	for e := range kw.Updates() {
		if e == nil {
			break // initial values done
		}
		b.handle(e)
	}
	if err := b.deleteOrphans(ctx); err != nil {
		b.log.Warn("deleting state of removed TVs", "error", err)
	}
	b.log.Info("bridge started", "bridge", b.opts.BridgeID, "devices", b.deviceCount())

	b.wg.Add(1)
	go b.watchDevices(runCtx, kw)
	if b.opts.HeartbeatInterval > 0 {
		b.wg.Add(1)
		go b.heartbeatLoop(runCtx)
	}
	return nil
}

// Stop stops the watcher, the heartbeat, any pairing, and every TV.
func (b *Bridge) Stop() {
	if b.cancel != nil {
		b.cancel()
	}
	b.wg.Wait()
	b.endPairing(nil)
	b.applyMu.Lock()
	defer b.applyMu.Unlock()
	b.mu.Lock()
	all := b.devices
	b.devices = map[string]*device{}
	b.mu.Unlock()
	for _, d := range all {
		d.stop()
	}
}

func bucket(ctx context.Context, js jetstream.JetStream, name, description string) (jetstream.KeyValue, error) {
	kv, err := js.KeyValue(ctx, name)
	if errors.Is(err, jetstream.ErrBucketNotFound) {
		kv, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: name, Description: description, History: 1})
	}
	if err != nil {
		return nil, fmt.Errorf("bucket %s: %w", name, err)
	}
	return kv, nil
}

func (b *Bridge) watchDevices(ctx context.Context, kw jetstream.KeyWatcher) {
	defer b.wg.Done()
	defer func() { _ = kw.Stop() }()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-kw.Updates():
			if !ok {
				return
			}
			if e != nil {
				b.handle(e)
			}
		}
	}
}

// handle applies one devices-bucket entry. Any change restarts that one TV;
// there is no diffing of old and new records.
func (b *Bridge) handle(e jetstream.KeyValueEntry) {
	id := strings.TrimPrefix(e.Key(), b.prefix())
	if e.Operation() != jetstream.KeyValuePut {
		b.remove(id)
		return
	}
	if !validDeviceID(id) {
		b.log.Warn("ignoring device key: the id must use only a-z, 0-9, - and _", "key", e.Key())
		return
	}
	rec, err := ParseRecord(e.Value())
	if err != nil {
		b.replace(id, invalidDevice(b, id, err))
		return
	}
	b.replace(id, newDevice(b, id, rec))
}

// replace stops the TV's current device, if any, and starts d in its place.
func (b *Bridge) replace(id string, d *device) {
	b.applyMu.Lock()
	defer b.applyMu.Unlock()
	b.mu.Lock()
	old := b.devices[id]
	delete(b.devices, id)
	b.mu.Unlock()
	if old != nil {
		old.stop()
	}
	b.mu.Lock()
	b.devices[id] = d
	b.mu.Unlock()
	d.start()
	b.countDevices()
}

// restart re-reads nothing: it starts the TV again from its current record.
// Pairing calls it so the TV connects with its new pairing at once.
func (b *Bridge) restart(id string) {
	d := b.device(id)
	if d == nil || d.snapshot().Error != "" {
		return
	}
	b.replace(id, newDevice(b, id, d.rec))
}

// remove stops the TV and deletes its state key.
func (b *Bridge) remove(id string) {
	b.applyMu.Lock()
	defer b.applyMu.Unlock()
	b.mu.Lock()
	old := b.devices[id]
	delete(b.devices, id)
	b.mu.Unlock()
	if old != nil {
		old.stop()
	}
	ctx, cancel := context.WithTimeout(context.Background(), b.opts.CommandTimeout)
	defer cancel()
	if err := b.stateKV.Delete(ctx, b.prefix()+id); err != nil {
		b.log.Warn("deleting state", "device", id, "error", err)
	}
	b.countDevices()
}

// deleteOrphans deletes state keys of TVs no longer in the devices bucket.
func (b *Bridge) deleteOrphans(ctx context.Context) error {
	lister, err := b.stateKV.ListKeysFiltered(ctx, b.prefix()+"*")
	if err != nil {
		if errors.Is(err, jetstream.ErrNoKeysFound) {
			return nil
		}
		return err
	}
	var orphans []string
	for k := range lister.Keys() {
		if b.device(strings.TrimPrefix(k, b.prefix())) == nil {
			orphans = append(orphans, k)
		}
	}
	for _, k := range orphans {
		if err := b.stateKV.Delete(ctx, k); err != nil {
			return fmt.Errorf("delete %s: %w", k, err)
		}
	}
	return nil
}

func (b *Bridge) device(id string) *device {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.devices[id]
}

// sortedDevices returns every device, sorted by id.
func (b *Bridge) sortedDevices() []*device {
	b.mu.Lock()
	out := make([]*device, 0, len(b.devices))
	for _, d := range b.devices {
		out = append(out, d)
	}
	b.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

func (b *Bridge) deviceCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.devices)
}

func (b *Bridge) putState(ctx context.Context, id string, s State) {
	data, err := json.Marshal(s)
	if err == nil {
		putCtx, cancel := context.WithTimeout(ctx, b.opts.CommandTimeout)
		_, err = b.stateKV.Put(putCtx, b.prefix()+id, data)
		cancel()
	}
	if err != nil {
		b.m.stateWrites.WithLabelValues("error").Inc()
		if ctx.Err() == nil {
			b.log.Warn("writing state", "device", id, "error", err)
		}
		return
	}
	b.m.stateWrites.WithLabelValues("ok").Inc()
}

// RewriteAll writes every TV's current state again. Puts made while NATS was
// disconnected failed, so this runs on reconnect.
func (b *Bridge) RewriteAll() {
	for _, d := range b.sortedDevices() {
		d.markDirty()
	}
}

func (b *Bridge) countDevices() {
	counts := map[string]float64{"connected": 0, "unreachable": 0, "unpaired": 0, "invalid": 0}
	for _, d := range b.sortedDevices() {
		counts[d.snapshot().status()]++
	}
	for status, n := range counts {
		b.m.devices.WithLabelValues(status).Set(n)
	}
}

type heartbeat struct {
	Bridge    string `json:"bridge"`
	Devices   int    `json:"devices"`
	Connected int    `json:"connected"`
}

func (b *Bridge) heartbeatLoop(ctx context.Context) {
	defer b.wg.Done()
	t := time.NewTicker(b.opts.HeartbeatInterval)
	defer t.Stop()
	for {
		b.sendHeartbeat()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (b *Bridge) sendHeartbeat() {
	hb := heartbeat{Bridge: b.opts.BridgeID}
	for _, d := range b.sortedDevices() {
		hb.Devices++
		if d.snapshot().Available {
			hb.Connected++
		}
	}
	data, _ := json.Marshal(hb)
	if err := b.nc.Publish("atv."+b.opts.BridgeID+".heartbeat", data); err != nil {
		b.log.Warn("heartbeat", "error", err)
	}
}

// Ready reports the bridge's health for /api/ready. A TV that is off or
// unreachable doesn't fail readiness: TVs are turned off all the time.
func (b *Bridge) Ready() []health.Result {
	var out []health.Result
	switch {
	case b.nc == nil:
		out = append(out, health.Result{Name: "nats", State: health.Fail, Detail: "not connected yet"})
	case !b.nc.IsConnected():
		out = append(out, health.Result{Name: "nats", State: health.Fail, Detail: "disconnected: " + b.nc.Status().String(),
			Fix: "Check nats.url and that the server is reachable"})
	default:
		out = append(out, health.Result{Name: "nats", State: health.OK, Detail: "connected to " + b.nc.ConnectedUrlRedacted()})
	}

	if b.bound.Load() {
		out = append(out, health.Result{Name: "buckets", State: health.OK, Detail: b.opts.DevicesBucket + ", " + b.opts.StateBucket})
	} else {
		out = append(out, health.Result{Name: "buckets", State: health.Fail, Detail: "not bound yet"})
	}

	counts := map[string]int{}
	var invalid []string
	devices := b.sortedDevices()
	for _, d := range devices {
		st := d.snapshot().status()
		counts[st]++
		if st == "invalid" {
			invalid = append(invalid, d.id)
		}
	}
	detail := fmt.Sprintf("%d connected, %d unreachable, %d unpaired, %d invalid",
		counts["connected"], counts["unreachable"], counts["unpaired"], counts["invalid"])
	if len(invalid) > 0 {
		out = append(out, health.Result{Name: "devices", State: health.Warn, Detail: detail + ": " + strings.Join(invalid, ", "),
			Fix: "Each invalid TV's error is in the state bucket"})
	} else {
		out = append(out, health.Result{Name: "devices", State: health.OK, Detail: detail})
	}

	if _, err := os.Stat(b.certFile()); err == nil {
		out = append(out, health.Result{Name: "certificate", State: health.OK, Detail: b.certFile()})
	} else if len(devices) > 0 {
		out = append(out, health.Result{Name: "certificate", State: health.Warn, Detail: "no client certificate in " + b.opts.SessionDir,
			Fix: "Pair a TV; if TVs were paired before, session_dir was lost and each TV must be paired again"})
	} else {
		out = append(out, health.Result{Name: "certificate", State: health.Skipped, Detail: "no TVs yet"})
	}
	return out
}
