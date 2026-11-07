package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/stone-age-io/atv-bridge/config"
	"github.com/stone-age-io/atv-bridge/third_party/atvremote/remote"
)

// Record is one TV in the devices bucket.
type Record struct {
	Host string `json:"host"`
	Name string `json:"name,omitempty"`
}

// ParseRecord parses and checks a devices-bucket value. Unknown fields are an
// error, so a typo shows up in the TV's state instead of being ignored.
func ParseRecord(data []byte) (Record, error) {
	var r Record
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return Record{}, fmt.Errorf("invalid device record: %w", err)
	}
	if strings.TrimSpace(r.Host) == "" {
		return Record{}, errors.New("host is required")
	}
	return r, nil
}

// State is one TV in the state bucket, and the reply to the state endpoint.
// A bad record has only Error and Since.
type State struct {
	Name      string             `json:"name,omitempty"`
	Host      string             `json:"host,omitempty"`
	Available bool               `json:"available"`
	Paired    bool               `json:"paired"`
	On        bool               `json:"on"`
	App       string             `json:"app,omitempty"`
	Volume    *remote.VolumeInfo `json:"volume,omitempty"`
	Device    *remote.DeviceInfo `json:"device,omitempty"`
	Error     string             `json:"error,omitempty"`
	Since     time.Time          `json:"since"`
}

// status is the device's label in the atv_devices metric and devices reply.
func (s State) status() string {
	switch {
	case s.Error != "":
		return "invalid"
	case s.Available:
		return "connected"
	case !s.Paired:
		return "unpaired"
	default:
		return "unreachable"
	}
}

// device is one TV: a goroutine that keeps it connected, and a goroutine
// that writes its latest state to KV whenever it changes.
type device struct {
	id  string
	rec Record
	b   *Bridge

	mu     sync.Mutex
	st     State
	remote *remote.AndroidTVRemote // nil unless connected

	cmdMu  sync.Mutex    // commands to one TV run in order
	dirty  chan struct{} // state changed; buffered 1, so writes coalesce
	cancel context.CancelFunc
	done   sync.WaitGroup
}

func newDevice(b *Bridge, id string, rec Record) *device {
	name := rec.Name
	if name == "" {
		name = id
	}
	return &device{
		id: id, rec: rec, b: b,
		st:    State{Name: name, Host: rec.Host, Since: time.Now().UTC()},
		dirty: make(chan struct{}, 1),
	}
}

// invalidDevice is a device whose record didn't parse. It runs only the
// state writer, so the error lands in KV where the console looks.
func invalidDevice(b *Bridge, id string, err error) *device {
	d := &device{id: id, b: b, dirty: make(chan struct{}, 1)}
	d.st = State{Error: err.Error(), Since: time.Now().UTC()}
	return d
}

func (d *device) start() {
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	d.done.Add(1)
	go d.writeLoop(ctx)
	if d.st.Error == "" {
		d.done.Add(1)
		go d.run(ctx)
	}
	d.markDirty()
}

// stop ends both goroutines and the TV connection. The last state may not
// be written; the caller deletes or rewrites the key.
func (d *device) stop() {
	d.cancel()
	d.done.Wait()
}

func (d *device) markDirty() {
	select {
	case d.dirty <- struct{}{}:
	default:
	}
}

func (d *device) snapshot() State {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.st
}

func (d *device) connected() *remote.AndroidTVRemote {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.remote
}

// update changes the state with fn and schedules a write if anything other
// than Since changed.
func (d *device) update(fn func(*State)) {
	d.mu.Lock()
	before := d.st
	fn(&d.st)
	after := d.st
	after.Since = before.Since
	changed := !equalState(before, after)
	if changed {
		d.st.Since = time.Now().UTC()
	}
	d.mu.Unlock()
	if changed {
		d.markDirty()
		d.b.countDevices()
	}
}

func equalState(a, b State) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

func (d *device) writeLoop(ctx context.Context) {
	defer d.done.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.dirty:
			d.b.putState(ctx, d.id, d.snapshot())
		}
	}
}

// run keeps the TV connected until ctx ends. After the first connect, the
// library's own loop handles reconnects; this loop only takes over when the
// library gives up because the TV no longer accepts our certificate.
func (d *device) run(ctx context.Context) {
	defer d.done.Done()
	delay := d.b.opts.RetryMin
	for ctx.Err() == nil {
		r := d.b.newRemote(d.id, d.rec.Host)
		removeCallbacks := d.watch(r)

		connCtx, cancel := context.WithTimeout(ctx, d.b.opts.CommandTimeout)
		err := r.Connect(connCtx)
		cancel()
		if err == nil {
			delay = d.b.opts.RetryMin
			d.serve(ctx, r)
			removeCallbacks()
			if !sleep(ctx, d.b.opts.PairRetry) {
				return
			}
			continue
		}
		r.Disconnect()
		removeCallbacks()

		var invalidAuth *remote.InvalidAuth
		if errors.As(err, &invalidAuth) {
			d.update(func(s *State) { s.Available, s.Paired = false, false })
			if !sleep(ctx, d.b.opts.PairRetry) {
				return
			}
			continue
		}
		d.b.log.Debug("connect failed", "device", d.id, "error", err)
		d.update(func(s *State) { s.Available = false })
		if !sleep(ctx, delay) {
			return
		}
		delay = min(2*delay, d.b.opts.RetryMax)
	}
}

// serve runs while r is connected or reconnecting. It returns when ctx ends
// or the TV rejects our certificate.
func (d *device) serve(ctx context.Context, r *remote.AndroidTVRemote) {
	d.mu.Lock()
	d.remote = r
	d.mu.Unlock()
	d.refresh(r, true)

	authLost := make(chan struct{})
	var once sync.Once
	r.KeepReconnecting(func() { once.Do(func() { close(authLost) }) })

	select {
	case <-ctx.Done():
	case <-authLost:
		d.update(func(s *State) { s.Available, s.Paired = false, false })
	}
	d.mu.Lock()
	d.remote = nil
	d.mu.Unlock()
	r.Disconnect()
}

// refresh copies everything the library knows into the state.
func (d *device) refresh(r *remote.AndroidTVRemote, available bool) {
	on, _ := r.IsOn()
	app, _ := r.CurrentApp()
	vol, info := r.VolumeInfo(), r.DeviceInfo()
	d.update(func(s *State) {
		s.Available, s.Paired = available, true
		s.On, s.App = on, app
		if vol != nil {
			s.Volume = vol
		}
		if info != nil {
			s.Device = info
		}
	})
}

// watch registers state callbacks on r and returns a function that removes
// them.
func (d *device) watch(r *remote.AndroidTVRemote) (remove func()) {
	removes := []func(){
		r.AddIsOnUpdatedCallback(func(on bool) { d.update(func(s *State) { s.On = on }) }),
		r.AddCurrentAppUpdatedCallback(func(app string) { d.update(func(s *State) { s.App = app }) }),
		r.AddVolumeInfoUpdatedCallback(func(v remote.VolumeInfo) { d.update(func(s *State) { s.Volume = &v }) }),
		r.AddIsAvailableUpdatedCallback(func(ok bool) {
			if ok {
				d.refresh(r, true)
			} else {
				d.update(func(s *State) { s.Available = false })
			}
		}),
	}
	return func() {
		for _, rm := range removes {
			rm()
		}
	}
}

// sleep waits for d or until ctx ends, and reports whether ctx is still live.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// validDeviceID reports whether id can be a device id: one subject token.
func validDeviceID(id string) bool { return config.ValidToken(id) }
