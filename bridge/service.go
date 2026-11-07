package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go/micro"

	"github.com/stone-age-io/atv-bridge/third_party/atvremote/remote"
)

// AddService registers the bridge as a NATS micro service. Every endpoint has
// a fixed wildcard subject, atv.<bridge>.*.<verb>, so a TV added in KV is
// reachable at once without registering anything.
func (b *Bridge) AddService(version string) (micro.Service, error) {
	svc, err := micro.AddService(b.nc, micro.Config{
		Name:        "atv-bridge",
		Version:     version,
		Description: "Controls Google TV and Android TV devices",
		Metadata:    map[string]string{"bridge": b.opts.BridgeID},
	})
	if err != nil {
		return nil, err
	}
	base := "atv." + b.opts.BridgeID
	endpoints := []struct {
		name, subject string
		h             micro.HandlerFunc
	}{
		{"key", base + ".*.key", b.deviceEndpoint("key", connectedOnly(handleKey))},
		{"text", base + ".*.text", b.deviceEndpoint("text", connectedOnly(handleText))},
		{"launch", base + ".*.launch", b.deviceEndpoint("launch", connectedOnly(handleLaunch))},
		{"power", base + ".*.power", b.deviceEndpoint("power", connectedOnly(handlePower))},
		{"state", base + ".*.state", b.deviceEndpoint("state", handleState)},
		{"pair_start", base + ".*.pair.start", b.deviceEndpoint("pair_start", b.handlePairStart)},
		{"pair_finish", base + ".*.pair.finish", b.deviceEndpoint("pair_finish", b.handlePairFinish)},
		{"devices", base + ".devices", b.handleDevices},
	}
	for _, e := range endpoints {
		if err := svc.AddEndpoint(e.name, e.h, micro.WithEndpointSubject(e.subject)); err != nil {
			_ = svc.Stop()
			return nil, err
		}
	}
	return svc, nil
}

// reqError is a failed request: a micro error code and a description.
type reqError struct{ code, msg string }

func badRequest(format string, a ...any) *reqError {
	return &reqError{"400", fmt.Sprintf(format, a...)}
}

type deviceHandler func(d *device, data []byte) (any, *reqError)

// deviceEndpoint looks up the TV named by the subject's third token, runs fn
// with the TV's commands serialised, and replies.
func (b *Bridge) deviceEndpoint(verb string, fn deviceHandler) micro.HandlerFunc {
	return func(req micro.Request) {
		tokens := strings.Split(req.Subject(), ".")
		id := tokens[2]
		d := b.device(id)
		var res any
		var e *reqError
		switch {
		case d == nil:
			e = &reqError{"404", "unknown device " + id}
		case d.snapshot().Error != "":
			e = &reqError{"409", "device record is invalid: " + d.snapshot().Error}
		default:
			d.cmdMu.Lock()
			res, e = fn(d, req.Data())
			d.cmdMu.Unlock()
		}
		b.reply(req, verb, res, e)
	}
}

func (b *Bridge) reply(req micro.Request, verb string, res any, e *reqError) {
	if e != nil {
		b.m.commands.WithLabelValues(verb, e.code).Inc()
		_ = req.Error(e.code, e.msg, nil)
		return
	}
	b.m.commands.WithLabelValues(verb, "ok").Inc()
	if res == nil {
		res = map[string]any{"ok": true}
	}
	_ = req.RespondJSON(res)
}

// connectedOnly runs fn only when the TV is connected.
func connectedOnly(fn func(r *remote.AndroidTVRemote, data []byte) (any, *reqError)) deviceHandler {
	return func(d *device, data []byte) (any, *reqError) {
		r := d.connected()
		if r == nil {
			if !d.snapshot().Paired {
				return nil, &reqError{"409", "not paired: call pair.start"}
			}
			return nil, &reqError{"503", "TV is not connected"}
		}
		res, e := fn(r, data)
		if e == nil {
			return res, nil
		}
		return nil, e
	}
}

// decode parses a JSON request body into v. An empty body is an empty
// object.
func decode(data []byte, v any) *reqError {
	if len(data) == 0 {
		data = []byte("{}")
	}
	if err := json.Unmarshal(data, v); err != nil {
		return badRequest("invalid JSON: %v", err)
	}
	return nil
}

// sendErr maps a library error from a send to a reply.
func sendErr(err error) *reqError {
	if err == nil {
		return nil
	}
	return &reqError{"503", err.Error()}
}

func handleKey(r *remote.AndroidTVRemote, data []byte) (any, *reqError) {
	var req struct {
		Key       string `json:"key"`
		Direction string `json:"direction"`
	}
	if e := decode(data, &req); e != nil {
		return nil, e
	}
	if req.Direction == "" {
		req.Direction = "SHORT"
	}
	code, err := remote.ParseKeyCode(req.Key)
	if err != nil || req.Key == "" {
		return nil, badRequest("unknown key %q", req.Key)
	}
	dir, err := remote.ParseDirection(req.Direction)
	if err != nil {
		return nil, badRequest("direction must be SHORT, START_LONG or END_LONG")
	}
	return nil, sendErr(r.SendKeyCode(code, dir))
}

func handleText(r *remote.AndroidTVRemote, data []byte) (any, *reqError) {
	var req struct {
		Text string `json:"text"`
	}
	if e := decode(data, &req); e != nil {
		return nil, e
	}
	if req.Text == "" {
		return nil, badRequest("text is required")
	}
	return nil, sendErr(r.SendText(req.Text))
}

func handleLaunch(r *remote.AndroidTVRemote, data []byte) (any, *reqError) {
	var req struct {
		App string `json:"app"`
	}
	if e := decode(data, &req); e != nil {
		return nil, e
	}
	if req.App == "" {
		return nil, badRequest("app is required: an app link or a package id")
	}
	return nil, sendErr(r.SendLaunchAppCommand(req.App))
}

// handlePower sends POWER only when the TV's state differs from the one
// asked for, so a rule can send the same request again and again.
func handlePower(r *remote.AndroidTVRemote, data []byte) (any, *reqError) {
	var req struct {
		On *bool `json:"on"`
	}
	if e := decode(data, &req); e != nil {
		return nil, e
	}
	if req.On == nil {
		return nil, badRequest(`on is required: {"on": true} or {"on": false}`)
	}
	if on, ok := r.IsOn(); ok && on == *req.On {
		return map[string]any{"ok": true, "changed": false}, nil
	}
	if err := r.SendKeyCommand("POWER"); err != nil {
		return nil, sendErr(err)
	}
	return map[string]any{"ok": true, "changed": true}, nil
}

func handleState(d *device, _ []byte) (any, *reqError) { return d.snapshot(), nil }

type deviceSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Host      string `json:"host,omitempty"`
	Status    string `json:"status"`
	Available bool   `json:"available"`
	Paired    bool   `json:"paired"`
	Error     string `json:"error,omitempty"`
}

func (b *Bridge) handleDevices(req micro.Request) {
	out := []deviceSummary{}
	for _, d := range b.sortedDevices() {
		s := d.snapshot()
		out = append(out, deviceSummary{ID: d.id, Name: s.Name, Host: s.Host, Status: s.status(),
			Available: s.Available, Paired: s.Paired, Error: s.Error})
	}
	b.reply(req, "devices", map[string]any{"devices": out}, nil)
}

// pairing is the one pairing in progress on this bridge.
type pairing struct {
	id    string
	r     *remote.AndroidTVRemote
	timer *time.Timer
}

// handlePairStart asks the TV to show a pairing code. One pairing at a time
// per bridge; starting again for the same TV replaces it, for when the code
// was missed.
func (b *Bridge) handlePairStart(d *device, _ []byte) (any, *reqError) {
	b.pairMu.Lock()
	defer b.pairMu.Unlock()
	if b.pair != nil && b.pair.id != d.id {
		return nil, &reqError{"409", fmt.Sprintf("pairing with %s is in progress; finish it or wait %s", b.pair.id, b.opts.PairTimeout)}
	}
	b.endPairingLocked()

	if err := os.MkdirAll(b.opts.SessionDir, 0o700); err != nil {
		return nil, &reqError{"500", "session_dir: " + err.Error()}
	}
	r := b.newRemote(d.id, d.rec.Host)
	created, err := r.GenerateCertIfMissing()
	if err != nil {
		return nil, &reqError{"500", "creating the client certificate: " + err.Error()}
	}
	if created {
		b.log.Info("created client certificate", "path", b.certFile())
	}
	ctx, cancel := context.WithTimeout(context.Background(), b.opts.CommandTimeout)
	defer cancel()
	if err := r.StartPairing(ctx); err != nil {
		r.Disconnect()
		return nil, &reqError{"503", "starting pairing: " + err.Error()}
	}
	p := &pairing{id: d.id, r: r}
	p.timer = time.AfterFunc(b.opts.PairTimeout, func() { b.endPairing(p) })
	b.pair = p
	b.log.Info("pairing started", "device", d.id)
	return nil, nil
}

// handlePairFinish sends the code from the TV. A code that doesn't match
// keeps the pairing open, so the person can try again.
func (b *Bridge) handlePairFinish(d *device, data []byte) (any, *reqError) {
	var req struct {
		PIN string `json:"pin"`
	}
	if e := decode(data, &req); e != nil {
		return nil, e
	}
	if req.PIN == "" {
		return nil, badRequest("pin is required: the code shown on the TV")
	}

	b.pairMu.Lock()
	defer b.pairMu.Unlock()
	p := b.pair
	if p == nil || p.id != d.id {
		return nil, &reqError{"409", "no pairing in progress for " + d.id + ": call pair.start"}
	}
	err := p.r.FinishPairing(strings.ToUpper(strings.TrimSpace(req.PIN)))
	var invalidAuth *remote.InvalidAuth
	switch {
	case errors.As(err, &invalidAuth):
		return nil, badRequest("wrong code: %v", err)
	case err != nil:
		b.endPairingLocked()
		return nil, &reqError{"503", "pairing failed: " + err.Error()}
	}
	b.endPairingLocked()
	b.log.Info("paired", "device", d.id)
	// The TV now accepts our certificate: connect at once instead of waiting
	// for the unpaired retry.
	b.restart(d.id)
	return nil, nil
}

// endPairing ends p if it is still the pairing in progress; nil ends any.
func (b *Bridge) endPairing(p *pairing) {
	b.pairMu.Lock()
	defer b.pairMu.Unlock()
	if p == nil || b.pair == p {
		b.endPairingLocked()
	}
}

func (b *Bridge) endPairingLocked() {
	if b.pair == nil {
		return
	}
	b.pair.timer.Stop()
	b.pair.r.Disconnect()
	b.pair = nil
}
