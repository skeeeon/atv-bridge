package remote

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	pb "github.com/stone-age-io/atv-bridge/third_party/atvremote/proto"
	"google.golang.org/protobuf/proto"
)

// Feature is a bitmask of remote control features negotiated with the device.
type Feature int32

const (
	FeaturePing Feature = 1 << 0
	FeatureKey  Feature = 1 << 1
	FeatureIME  Feature = 1 << 2
	// FeatureVoice enables remote_voice_begin after sending KEYCODE_SEARCH.
	FeatureVoice    Feature = 1 << 3
	FeatureUnknown1 Feature = 1 << 4
	FeaturePower    Feature = 1 << 5
	FeatureVolume   Feature = 1 << 6
	FeatureAppLink  Feature = 1 << 9
)

const (
	// The server pings every 5 seconds if there is no command sent and closes
	// connections after 3 unanswered pings; mirror that by disconnecting if
	// there is no message from the server or client within 16 seconds.
	idleTimeout = 16 * time.Second

	// VoiceSessionTimeout is the default timeout to wait for
	// remote_voice_begin after sending KEYCODE_SEARCH.
	VoiceSessionTimeout = 2 * time.Second
	// Voice data chunk size in bytes for the remote_voice_payload message.
	voiceChunkSize = 20 * 1024
	// Minimum voice data chunk size in bytes. Shield TV did not accept lower
	// chunk sizes.
	voiceChunkMinSize = 8 * 1024

	errorSuggestionMsg = "Try clearing the storage of the Android TV Remote Service system app. " +
		"On the Android TV device, go to Settings > Apps > See all apps > Show system apps. " +
		"Then, select Android TV Remote Service > Storage > Clear data/storage."
)

// remoteProtocol implements the remote protocol with an Android TV.
//
// Messages transmitted between client and server are of type RemoteMessage,
// see remotemessage.proto. Protocol is described in
// https://github.com/Aymkdn/assistant-freebox-cloud/wiki/Google-TV-(aka-Android-TV)-Remote-Control-(v2)
type remoteProtocol struct {
	conn   *protobufConn
	logger *slog.Logger

	onIsOnUpdated       func(bool)
	onCurrentAppUpdated func(string)
	onVolumeInfoUpdated func(VolumeInfo)

	mu              sync.Mutex
	activeFeatures  Feature
	isOn            bool
	currentApp      string
	deviceInfo      *DeviceInfo
	volumeInfo      *VolumeInfo
	imeCounter      int32
	imeFieldCounter int32
	voiceBegin      chan int32

	remoteStarted chan struct{}
	startedOnce   sync.Once

	conLost     chan struct{}
	conLostOnce sync.Once
	conLostErr  error

	idleTimer *time.Timer
}

func newRemoteProtocol(
	conn *protobufConn,
	logger *slog.Logger,
	enableIME, enableVoice bool,
	onIsOnUpdated func(bool),
	onCurrentAppUpdated func(string),
	onVolumeInfoUpdated func(VolumeInfo),
) *remoteProtocol {
	features := FeaturePing | FeatureKey | FeaturePower | FeatureVolume | FeatureAppLink
	if enableIME {
		features |= FeatureIME
	}
	if enableVoice {
		features |= FeatureVoice
	}
	p := &remoteProtocol{
		conn:                conn,
		logger:              logger,
		activeFeatures:      features,
		onIsOnUpdated:       onIsOnUpdated,
		onCurrentAppUpdated: onCurrentAppUpdated,
		onVolumeInfoUpdated: onVolumeInfoUpdated,
		remoteStarted:       make(chan struct{}),
		conLost:             make(chan struct{}),
	}
	p.idleTimer = time.AfterFunc(idleTimeout, func() {
		p.logger.Debug("Closing idle connection")
		p.connectionLost(errors.New("closed idle connection"))
	})
	return p
}

func (p *remoteProtocol) readLoop() {
	for {
		raw, err := p.conn.readRaw()
		if err != nil {
			p.connectionLost(err)
			return
		}
		p.resetIdleTimer()
		msg := &pb.RemoteMessage{}
		if err := proto.Unmarshal(raw, msg); err != nil {
			p.logger.Debug("Couldn't parse as RemoteMessage", "err", err)
			continue
		}
		p.handleMessage(msg)
	}
}

func (p *remoteProtocol) connectionLost(err error) {
	p.conLostOnce.Do(func() {
		p.logger.Debug("Connection lost", "err", err)
		p.conLostErr = err
		p.idleTimer.Stop()
		_ = p.conn.close()
		close(p.conLost)
	})
}

func (p *remoteProtocol) close() {
	p.connectionLost(errors.New("connection closed"))
}

func (p *remoteProtocol) closed() bool {
	select {
	case <-p.conLost:
		return true
	default:
		return false
	}
}

func (p *remoteProtocol) resetIdleTimer() {
	p.idleTimer.Reset(idleTimeout)
}

func (p *remoteProtocol) features() Feature {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.activeFeatures
}

func (p *remoteProtocol) isVoiceEnabled() bool {
	return p.features()&FeatureVoice == FeatureVoice
}

func (p *remoteProtocol) getIsOn() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.isOn
}

func (p *remoteProtocol) getCurrentApp() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.currentApp
}

// getDeviceInfo returns a copy of the device info, or nil if not received yet.
func (p *remoteProtocol) getDeviceInfo() *DeviceInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.deviceInfo == nil {
		return nil
	}
	info := *p.deviceInfo
	return &info
}

// getVolumeInfo returns a copy of the volume info, or nil if not received yet.
func (p *remoteProtocol) getVolumeInfo() *VolumeInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.volumeInfo == nil {
		return nil
	}
	info := *p.volumeInfo
	return &info
}

// send synchronously writes msg to the connection. Returns ConnectionClosed
// if the connection has been lost.
func (p *remoteProtocol) send(msg *pb.RemoteMessage, logSend bool) error {
	if logSend {
		logDebugProto(p.logger, "Sending", msg)
	}
	if p.closed() {
		p.logger.Debug("Connection is closed!")
		return &ConnectionClosed{Message: "connection is closed"}
	}
	p.resetIdleTimer()
	if err := p.conn.sendMessage(msg); err != nil {
		p.connectionLost(err)
		return &ConnectionClosed{Message: "connection is closed", Err: err}
	}
	return nil
}

func (p *remoteProtocol) handleMessage(msg *pb.RemoteMessage) {
	if msg.GetRemotePingRequest() == nil {
		logDebugProto(p.logger, "Received", msg)
	}

	switch {
	case msg.GetRemoteConfigure() != nil:
		cfg := msg.GetRemoteConfigure()
		p.mu.Lock()
		p.deviceInfo = &DeviceInfo{
			Manufacturer: cfg.GetDeviceInfo().GetVendor(),
			Model:        cfg.GetDeviceInfo().GetModel(),
			SWVersion:    cfg.GetDeviceInfo().GetAppVersion(),
		}
		supportedFeatures := Feature(cfg.GetCode1())
		p.activeFeatures &= supportedFeatures
		active := p.activeFeatures
		p.mu.Unlock()
		p.logger.Debug("Device supports", "features", fmt.Sprintf("%b", supportedFeatures))
		if supportedFeatures&FeatureKey == 0 {
			p.logger.Error("Device doesn't support sending keys. " + errorSuggestionMsg)
		}
		if supportedFeatures&FeatureAppLink == 0 {
			p.logger.Error("Device doesn't support sending app links. " + errorSuggestionMsg)
		}
		_ = p.send(&pb.RemoteMessage{RemoteConfigure: &pb.RemoteConfigure{
			Code1: int32(active),
			DeviceInfo: &pb.RemoteDeviceInfo{
				Unknown1:    1,
				Unknown2:    "1",
				PackageName: "atvremote",
				AppVersion:  "1.0.0",
			},
		}}, true)

	case msg.GetRemoteSetActive() != nil:
		_ = p.send(&pb.RemoteMessage{RemoteSetActive: &pb.RemoteSetActive{
			Active: int32(p.features()),
		}}, true)

	case msg.GetRemoteImeKeyInject() != nil:
		app := msg.GetRemoteImeKeyInject().GetAppInfo().GetAppPackage()
		p.mu.Lock()
		p.currentApp = app
		p.mu.Unlock()
		p.onCurrentAppUpdated(app)

	case msg.GetRemoteImeBatchEdit() != nil:
		p.mu.Lock()
		p.imeCounter = msg.GetRemoteImeBatchEdit().GetImeCounter()
		p.imeFieldCounter = msg.GetRemoteImeBatchEdit().GetFieldCounter()
		p.mu.Unlock()

	case msg.GetRemoteSetVolumeLevel() != nil:
		vol := msg.GetRemoteSetVolumeLevel()
		info := VolumeInfo{
			Level: vol.GetVolumeLevel(),
			Max:   vol.GetVolumeMax(),
			Muted: vol.GetVolumeMuted(),
		}
		p.mu.Lock()
		p.volumeInfo = &info
		p.mu.Unlock()
		p.onVolumeInfoUpdated(info)

	case msg.GetRemoteStart() != nil:
		isOn := msg.GetRemoteStart().GetStarted()
		p.mu.Lock()
		p.isOn = isOn
		p.mu.Unlock()
		p.startedOnce.Do(func() { close(p.remoteStarted) })
		p.onIsOnUpdated(isOn)

	case msg.GetRemotePingRequest() != nil:
		_ = p.send(&pb.RemoteMessage{RemotePingResponse: &pb.RemotePingResponse{
			Val1: msg.GetRemotePingRequest().GetVal1(),
		}}, false)

	case msg.GetRemoteVoiceBegin() != nil:
		p.mu.Lock()
		ch := p.voiceBegin
		p.mu.Unlock()
		if ch != nil {
			select {
			case ch <- msg.GetRemoteVoiceBegin().GetSessionId():
			default:
			}
		} else {
			p.logger.Debug("Ignoring remote_voice_begin: no client request available")
		}

	default:
		logDebugProto(p.logger, "Unhandled", msg)
	}
}

// sendKeyCode sends a key press to the Android TV.
func (p *remoteProtocol) sendKeyCode(keyCode pb.RemoteKeyCode, direction pb.RemoteDirection) error {
	return p.send(&pb.RemoteMessage{RemoteKeyInject: &pb.RemoteKeyInject{
		KeyCode:   keyCode,
		Direction: direction,
	}}, true)
}

// sendText sends a text string to the Android TV via the input method.
//
// The text length is used for both start and end in the RemoteImeObject.
// The imeCounter and imeFieldCounter values are taken from the last received
// remote_ime_batch_edit message.
func (p *remoteProtocol) sendText(text string) error {
	if text == "" {
		return errors.New("text cannot be empty")
	}
	paramValue := int32(utf8.RuneCountInString(text) - 1)
	p.mu.Lock()
	imeCounter := p.imeCounter
	imeFieldCounter := p.imeFieldCounter
	p.mu.Unlock()
	return p.send(&pb.RemoteMessage{RemoteImeBatchEdit: &pb.RemoteImeBatchEdit{
		ImeCounter:   imeCounter,
		FieldCounter: imeFieldCounter,
		EditInfo: []*pb.RemoteEditInfo{{
			Insert: 1,
			TextFieldStatus: &pb.RemoteImeObject{
				Start: paramValue,
				End:   paramValue,
				Value: text,
			},
		}},
	}}, true)
}

// sendLaunchApp launches an app on the Android TV.
func (p *remoteProtocol) sendLaunchApp(appLink string) error {
	return p.send(&pb.RemoteMessage{RemoteAppLinkLaunchRequest: &pb.RemoteAppLinkLaunchRequest{
		AppLink: appLink,
	}}, true)
}

// startVoice initiates a voice session and returns the session id when ready.
//
// Sends KEYCODE_SEARCH and waits for remote_voice_begin. Also sends the
// initial remote_voice_begin message back to the device, as required by the
// protocol, so the caller can start streaming audio chunks.
func (p *remoteProtocol) startVoice(ctx context.Context, timeout time.Duration) (int32, error) {
	if p.closed() {
		return 0, &ConnectionClosed{Message: "connection has been lost"}
	}
	if timeout <= 0 {
		timeout = VoiceSessionTimeout
	}

	p.mu.Lock()
	if p.voiceBegin != nil {
		p.mu.Unlock()
		return 0, errors.New("voice session already in progress")
	}
	ch := make(chan int32, 1)
	p.voiceBegin = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.voiceBegin = nil
		p.mu.Unlock()
	}()

	if err := p.sendKeyCode(pb.RemoteKeyCode_KEYCODE_SEARCH, pb.RemoteDirection_SHORT); err != nil {
		return 0, err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case sessionID := <-ch:
		begin := &pb.RemoteMessage{RemoteVoiceBegin: &pb.RemoteVoiceBegin{SessionId: sessionID}}
		if err := p.send(begin, true); err != nil {
			return 0, err
		}
		return sessionID, nil
	case <-p.conLost:
		return 0, &ConnectionClosed{Message: "no voice session available", Err: p.conLostErr}
	case <-timer.C:
		p.logger.Debug("Timeout reached", "timeout", timeout)
		return 0, fmt.Errorf("voice session timed out after %s", timeout)
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// sendVoiceChunk sends a chunk of PCM audio for the active voice session.
func (p *remoteProtocol) sendVoiceChunk(chunk []byte, sessionID int32) error {
	if p.closed() {
		return &ConnectionClosed{Message: "connection has been lost"}
	}

	// Pad chunk to minimum chunk size.
	if len(chunk) < voiceChunkMinSize {
		chunk = append(chunk[:len(chunk):len(chunk)], make([]byte, voiceChunkMinSize-len(chunk))...)
	}

	// Limit chunk size, otherwise Android TV will close the connection.
	for i := 0; i < len(chunk); i += voiceChunkSize {
		end := min(i+voiceChunkSize, len(chunk))
		msg := &pb.RemoteMessage{RemoteVoicePayload: &pb.RemoteVoicePayload{
			SessionId: sessionID,
			Samples:   chunk[i:end],
		}}
		// Disable logging of voice data.
		if err := p.send(msg, false); err != nil {
			return err
		}
	}
	return nil
}

// endVoice ends the specified voice session.
func (p *remoteProtocol) endVoice(sessionID int32) error {
	return p.send(&pb.RemoteMessage{RemoteVoiceEnd: &pb.RemoteVoiceEnd{
		SessionId: sessionID,
	}}, true)
}
