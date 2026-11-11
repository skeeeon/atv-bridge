// Package remote implements pairing and connecting to an Android TV for
// remotely sending commands to it.
//
// It is a Go port of the Python library
// https://github.com/tronikos/androidtvremote2 with feature parity.
package remote

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	pb "github.com/skeeeon/atv-bridge/third_party/atvremote/proto"
)

// reconnectTimeout bounds one reconnect attempt of the reconnect loop.
const reconnectTimeout = 10 * time.Second

// oidDNQualifier is the ASN.1 object identifier of the dnQualifier attribute.
var oidDNQualifier = []int{2, 5, 4, 46}

// AndroidTVRemote pairs and connects to an Android TV for remotely sending
// commands to it.
type AndroidTVRemote struct {
	clientName  string
	certFile    string
	keyFile     string
	host        string
	apiPort     int
	pairPort    int
	enableIME   bool
	enableVoice bool
	logger      *slog.Logger

	mu              sync.Mutex
	proto           *remoteProtocol
	pairing         *pairingSession
	reconnectCancel context.CancelFunc
	tlsConfig       *tls.Config

	isOnCbs        callbacks[bool]
	currentAppCbs  callbacks[string]
	volumeInfoCbs  callbacks[VolumeInfo]
	isAvailableCbs callbacks[bool]
}

// Option configures an AndroidTVRemote.
type Option func(*AndroidTVRemote)

// WithAPIPort sets the port for connecting and sending commands (default 6466).
func WithAPIPort(port int) Option {
	return func(r *AndroidTVRemote) { r.apiPort = port }
}

// WithPairPort sets the port for pairing (default 6467).
func WithPairPort(port int) Option {
	return func(r *AndroidTVRemote) { r.pairPort = port }
}

// WithIME enables or disables the IME feature (default enabled). It is needed
// for getting the current app. Disable for devices that show 'Use keyboard on
// mobile device screen'.
func WithIME(enabled bool) Option {
	return func(r *AndroidTVRemote) { r.enableIME = enabled }
}

// WithVoice enables sending voice commands to the device (default disabled).
func WithVoice(enabled bool) Option {
	return func(r *AndroidTVRemote) { r.enableVoice = enabled }
}

// WithLogger sets the logger used for debug logging (default slog.Default()).
func WithLogger(logger *slog.Logger) Option {
	return func(r *AndroidTVRemote) { r.logger = logger }
}

// New creates an AndroidTVRemote.
//
// clientName is shown on the Android TV during pairing. certFile and keyFile
// are the paths of the client certificate and private key in PEM format
// (created during pairing). host is the IP address of the Android TV.
func New(clientName, certFile, keyFile, host string, opts ...Option) *AndroidTVRemote {
	r := &AndroidTVRemote{
		clientName: clientName,
		certFile:   certFile,
		keyFile:    keyFile,
		host:       host,
		apiPort:    6466,
		pairPort:   6467,
		enableIME:  true,
		logger:     slog.Default(),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Host returns the IP address of the Android TV.
func (r *AndroidTVRemote) Host() string { return r.host }

// IsOn reports whether the Android TV is on or off. ok is false if not
// connected.
func (r *AndroidTVRemote) IsOn() (isOn bool, ok bool) {
	p := r.protocol()
	if p == nil {
		return false, false
	}
	return p.getIsOn(), true
}

// CurrentApp returns the current app in the foreground on the Android TV,
// e.g. "com.google.android.youtube.tv". ok is false if not connected.
func (r *AndroidTVRemote) CurrentApp() (app string, ok bool) {
	p := r.protocol()
	if p == nil {
		return "", false
	}
	return p.getCurrentApp(), true
}

// DeviceInfo returns device info (manufacturer, model, sw_version), or nil if
// not connected or not received yet.
func (r *AndroidTVRemote) DeviceInfo() *DeviceInfo {
	p := r.protocol()
	if p == nil {
		return nil
	}
	return p.getDeviceInfo()
}

// VolumeInfo returns volume info (level, max, muted), or nil if not connected
// or not received yet.
func (r *AndroidTVRemote) VolumeInfo() *VolumeInfo {
	p := r.protocol()
	if p == nil {
		return nil
	}
	return p.getVolumeInfo()
}

// IsVoiceEnabled reports whether voice commands are enabled on the Android
// TV. Depends on the requested feature at initialization and the supported
// features of the device. ok is false if not connected.
func (r *AndroidTVRemote) IsVoiceEnabled() (enabled bool, ok bool) {
	p := r.protocol()
	if p == nil {
		return false, false
	}
	return p.isVoiceEnabled(), true
}

// AddIsOnUpdatedCallback adds a callback for when IsOn is updated. The
// returned function removes the callback again.
func (r *AndroidTVRemote) AddIsOnUpdatedCallback(callback func(bool)) (remove func()) {
	return r.isOnCbs.add(callback)
}

// AddCurrentAppUpdatedCallback adds a callback for when CurrentApp is
// updated. The returned function removes the callback again.
func (r *AndroidTVRemote) AddCurrentAppUpdatedCallback(callback func(string)) (remove func()) {
	return r.currentAppCbs.add(callback)
}

// AddVolumeInfoUpdatedCallback adds a callback for when VolumeInfo is
// updated. The returned function removes the callback again.
func (r *AndroidTVRemote) AddVolumeInfoUpdatedCallback(callback func(VolumeInfo)) (remove func()) {
	return r.volumeInfoCbs.add(callback)
}

// AddIsAvailableUpdatedCallback adds a callback for when the Android TV is
// ready to receive commands or is unavailable, fired by the reconnect loop
// started with KeepReconnecting. The returned function removes the callback
// again.
func (r *AndroidTVRemote) AddIsAvailableUpdatedCallback(callback func(bool)) (remove func()) {
	return r.isAvailableCbs.add(callback)
}

// GenerateCertIfMissing generates a client certificate and private key if
// missing. Returns true if a new certificate was generated.
func (r *AndroidTVRemote) GenerateCertIfMissing() (bool, error) {
	if fileExists(r.certFile) && fileExists(r.keyFile) {
		return false, nil
	}
	certPEM, keyPEM, err := GenerateSelfSignedCert(r.clientName)
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(r.certFile, certPEM, 0o644); err != nil {
		return false, err
	}
	if err := os.WriteFile(r.keyFile, keyPEM, 0o600); err != nil {
		return false, err
	}
	return true, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func (r *AndroidTVRemote) protocol() *remoteProtocol {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.proto
}

// connectedProtocol returns the current protocol or a ConnectionClosed error
// naming the operation if disconnected.
func (r *AndroidTVRemote) connectedProtocol(op string) (*remoteProtocol, error) {
	p := r.protocol()
	if p == nil {
		r.logger.Debug("Called " + op + " after disconnect")
		return nil, &ConnectionClosed{Message: "called " + op + " after disconnect"}
	}
	return p, nil
}

func (r *AndroidTVRemote) loadTLSConfig() (*tls.Config, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tlsConfig != nil {
		return r.tlsConfig, nil
	}
	// tls.LoadX509KeyPair populates cert.Leaf, which pairing needs to
	// compute the secret from the client public key.
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		r.logger.Debug("Missing certificate", "err", err)
		return nil, &InvalidAuth{Message: "missing certificate", Err: err}
	}
	r.tlsConfig = &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true, //nolint:gosec // Android TVs use self signed certificates.
	}
	return r.tlsConfig, nil
}

// dialTLS connects to the given port and performs the TLS handshake. A
// missing client certificate is returned as InvalidAuth and a TCP dial
// failure as CannotConnect; a handshake failure is returned unwrapped, for
// the caller to classify (Connect treats a TLS-level failure as needing to
// pair and anything else as a connection problem, the pairing port dials
// always as a connection problem).
func (r *AndroidTVRemote) dialTLS(ctx context.Context, port int) (*tls.Conn, error) {
	tlsConfig, err := r.loadTLSConfig()
	if err != nil {
		return nil, err
	}
	addr := net.JoinHostPort(r.host, strconv.Itoa(port))
	dialer := &net.Dialer{}
	rawConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		r.logger.Debug("Couldn't connect", "addr", addr, "err", err)
		return nil, &CannotConnect{Message: fmt.Sprintf("couldn't connect to %s", addr), Err: err}
	}
	tlsConn := tls.Client(rawConn, tlsConfig)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = rawConn.Close()
		r.logger.Debug("TLS handshake failed", "addr", addr, "err", err)
		return nil, fmt.Errorf("tls handshake with %s: %w", addr, err)
	}
	return tlsConn, nil
}

// isClassified reports whether err is already one of the typed errors of
// this package.
func isClassified(err error) bool {
	var cannotConnect *CannotConnect
	var invalidAuth *InvalidAuth
	return errors.As(err, &cannotConnect) || errors.As(err, &invalidAuth)
}

// dialPairPort dials the pairing port. Unlike Connect, a TLS handshake
// failure while pairing is a connection problem, not an authentication
// problem.
func (r *AndroidTVRemote) dialPairPort(ctx context.Context) (*tls.Conn, error) {
	tlsConn, err := r.dialTLS(ctx, r.pairPort)
	if err != nil {
		if isClassified(err) {
			return nil, err
		}
		return nil, &CannotConnect{Message: fmt.Sprintf("couldn't connect to %s:%d", r.host, r.pairPort), Err: err}
	}
	return tlsConn, nil
}

// Connect connects to the Android TV and waits until it is ready to receive
// commands.
//
// Returns CannotConnect if it couldn't connect (e.g. invalid IP address),
// ConnectionClosed if the connection was lost while waiting for the remote to
// start and InvalidAuth if pairing is needed first.
func (r *AndroidTVRemote) Connect(ctx context.Context) error {
	tlsConn, err := r.dialTLS(ctx, r.apiPort)
	if err != nil {
		if isClassified(err) {
			return err
		}
		// The handshake failed. Only a TLS-level failure means the device
		// rejected our certificate; a timeout, EOF or reset means it isn't
		// answering, which pairing again would not fix.
		if isTLSError(errors.Unwrap(err)) {
			return &InvalidAuth{Message: "need to pair", Err: err}
		}
		return &CannotConnect{Message: fmt.Sprintf("%s:%d didn't complete the TLS handshake", r.host, r.apiPort), Err: err}
	}
	r.logger.Debug("Connected", "peer", tlsConn.RemoteAddr())

	p := newRemoteProtocol(
		newProtobufConn(tlsConn),
		r.logger,
		r.enableIME,
		r.enableVoice,
		r.isOnCbs.fire,
		r.currentAppCbs.fire,
		r.volumeInfoCbs.fire,
	)
	go p.readLoop()

	select {
	case <-p.remoteStarted:
	case <-p.conLost:
		err := p.conLostErr
		r.logger.Debug("Couldn't connect", "host", r.host, "err", err)
		if isTLSError(err) {
			return &InvalidAuth{Message: "need to pair again", Err: err}
		}
		return &ConnectionClosed{Message: "connection closed", Err: err}
	case <-ctx.Done():
		p.close()
		return ctx.Err()
	}

	r.mu.Lock()
	old := r.proto
	r.proto = p
	r.mu.Unlock()
	if old != nil {
		old.close()
	}
	return nil
}

// isTLSError reports whether err was caused by the peer rejecting our
// certificate at the TLS layer, which means pairing is needed.
func isTLSError(err error) bool {
	if err == nil {
		return false
	}
	var recordErr tls.RecordHeaderError
	if errors.As(err, &recordErr) {
		return true
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return true
	}
	return strings.Contains(err.Error(), "tls:")
}

// KeepReconnecting starts a goroutine to keep reconnecting whenever the
// connection is lost, with exponential backoff. IsAvailable callbacks are
// fired on disconnect and on successful reconnect. If reconnecting fails
// because pairing is needed again, invalidAuthCallback (may be nil) is called
// and the loop stops.
func (r *AndroidTVRemote) KeepReconnecting(invalidAuthCallback func()) {
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	if r.reconnectCancel != nil {
		r.reconnectCancel()
	}
	r.reconnectCancel = cancel
	r.mu.Unlock()
	go r.reconnectLoop(ctx, invalidAuthCallback)
}

func (r *AndroidTVRemote) reconnectLoop(ctx context.Context, invalidAuthCallback func()) {
	for {
		p := r.protocol()
		if p == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-p.conLost:
		}
		r.isAvailableCbs.fire(false)
		r.logger.Debug("Disconnected", "host", r.host, "err", p.conLostErr)
		delay := 100 * time.Millisecond
		for {
			r.logger.Debug("Trying to reconnect", "host", r.host, "delay", delay)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			// Without a deadline, a device that accepts the TCP connection
			// but never answers would block this loop forever.
			attemptCtx, cancelAttempt := context.WithTimeout(ctx, reconnectTimeout)
			err := r.Connect(attemptCtx)
			cancelAttempt()
			if err == nil {
				r.isAvailableCbs.fire(true)
				break
			}
			var invalidAuth *InvalidAuth
			if errors.As(err, &invalidAuth) {
				r.logger.Debug("Couldn't reconnect. Won't retry", "host", r.host, "err", err)
				if invalidAuthCallback != nil {
					invalidAuthCallback()
				}
				return
			}
			delay = min(2*delay, 30*time.Second)
			r.logger.Debug("Couldn't reconnect. Will retry", "host", r.host, "delay", delay, "err", err)
		}
	}
}

// Disconnect disconnects any open connections and stops the reconnect loop.
func (r *AndroidTVRemote) Disconnect() {
	r.mu.Lock()
	if r.reconnectCancel != nil {
		r.reconnectCancel()
		r.reconnectCancel = nil
	}
	p := r.proto
	r.proto = nil
	pairing := r.pairing
	r.pairing = nil
	r.mu.Unlock()
	if p != nil {
		p.close()
	}
	if pairing != nil {
		pairing.close()
	}
}

// NameAndMAC connects to the Android TV and gets its name and MAC address
// from its certificate.
//
// Returns CannotConnect if it couldn't connect, e.g. invalid IP address.
func (r *AndroidTVRemote) NameAndMAC(ctx context.Context) (name string, mac string, err error) {
	tlsConn, err := r.dialPairPort(ctx)
	if err != nil {
		return "", "", err
	}
	peerCerts := tlsConn.ConnectionState().PeerCertificates
	_ = tlsConn.Close()
	if len(peerCerts) == 0 {
		return "", "", &CannotConnect{Message: "no server certificate"}
	}
	serverCert := peerCerts[0]

	// NVIDIA SHIELD example:
	// CN=atvremote/darcy/darcy/SHIELD Android TV/XX:XX:XX:XX:XX:XX
	// Nexus Player example:
	// dnQualifier=fugu/fugu/Nexus Player/CN=atvremote/XX:XX:XX:XX:XX:XX
	commonName := serverCert.Subject.CommonName
	dnQualifier := ""
	for _, attr := range serverCert.Subject.Names {
		if attr.Type.Equal(oidDNQualifier) {
			if s, ok := attr.Value.(string); ok {
				dnQualifier = s
			}
		}
	}
	commonNameParts := strings.Split(commonName, "/")
	dnQualifierParts := strings.Split(dnQualifier, "/")
	if dnQualifier != "" {
		name = dnQualifierParts[len(dnQualifierParts)-1]
	} else if len(commonNameParts) >= 2 {
		name = commonNameParts[len(commonNameParts)-2]
	}
	mac = commonNameParts[len(commonNameParts)-1]
	return name, mac, nil
}

// StartPairing starts the pairing process, after which the Android TV shows
// the pairing code on screen.
//
// Returns CannotConnect if it couldn't connect (e.g. invalid IP address) and
// ConnectionClosed if the connection was lost.
func (r *AndroidTVRemote) StartPairing(ctx context.Context) error {
	r.Disconnect()
	tlsConn, err := r.dialPairPort(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	session := &pairingSession{
		conn:       newProtobufConn(tlsConn),
		tlsConn:    tlsConn,
		clientName: r.clientName,
		clientCert: &r.tlsConfig.Certificates[0],
		logger:     r.logger,
	}
	r.pairing = session
	r.mu.Unlock()
	if err := session.start(); err != nil {
		r.Disconnect()
		return err
	}
	return nil
}

// FinishPairing finishes the pairing process using the pairing code shown on
// the Android TV.
//
// Returns ConnectionClosed if the connection was lost (e.g. user pressed
// cancel on the Android TV) and InvalidAuth if pairing was unsuccessful.
func (r *AndroidTVRemote) FinishPairing(pairingCode string) error {
	r.mu.Lock()
	session := r.pairing
	r.mu.Unlock()
	if session == nil {
		r.logger.Debug("Called FinishPairing after disconnect")
		return &ConnectionClosed{Message: "called FinishPairing after disconnect"}
	}
	err := session.finish(pairingCode)
	if err == nil {
		r.Disconnect()
	}
	return err
}

// SendKeyCommand sends a short key press to the Android TV.
//
// keyCode is a name from the RemoteKeyCode enum in remotemessage.proto (e.g.
// "KEYCODE_POWER" or just "POWER") or a string prefixed with "text:" to pass
// to SendText.
//
// Returns ConnectionClosed if the client is disconnected.
func (r *AndroidTVRemote) SendKeyCommand(keyCode string) error {
	return r.SendKeyCommandDirection(keyCode, "SHORT")
}

// SendKeyCommandDirection sends a key press to the Android TV with an
// explicit direction: "SHORT", "START_LONG" or "END_LONG".
//
// Returns ConnectionClosed if the client is disconnected.
func (r *AndroidTVRemote) SendKeyCommandDirection(keyCode, direction string) error {
	if strings.HasPrefix(strings.ToLower(keyCode), TextPrefix) {
		return r.SendText(keyCode[len(TextPrefix):])
	}
	code, err := ParseKeyCode(keyCode)
	if err != nil {
		return err
	}
	dir, err := ParseDirection(direction)
	if err != nil {
		return err
	}
	return r.SendKeyCode(code, dir)
}

// SendKeyCode sends a key press to the Android TV using enum values from the
// generated protobuf package.
//
// Returns ConnectionClosed if the client is disconnected.
func (r *AndroidTVRemote) SendKeyCode(keyCode pb.RemoteKeyCode, direction pb.RemoteDirection) error {
	p, err := r.connectedProtocol("SendKeyCode")
	if err != nil {
		return err
	}
	return p.sendKeyCode(keyCode, direction)
}

// SendText sends text to the Android TV via the input method.
//
// It may not work as expected if a virtual keyboard is present on the Android
// TV screen.
//
// Returns ConnectionClosed if the client is disconnected.
func (r *AndroidTVRemote) SendText(text string) error {
	p, err := r.connectedProtocol("SendText")
	if err != nil {
		return err
	}
	return p.sendText(text)
}

// SendLaunchAppCommand launches an app on the Android TV. Accepts an app link
// (e.g. "https://www.netflix.com/title.*") or an app id (e.g.
// "com.google.android.youtube.tv").
//
// Returns ConnectionClosed if the client is disconnected.
func (r *AndroidTVRemote) SendLaunchAppCommand(appLinkOrAppID string) error {
	p, err := r.connectedProtocol("SendLaunchAppCommand")
	if err != nil {
		return err
	}
	appLink := appLinkOrAppID
	if u, err := url.Parse(appLinkOrAppID); err != nil || u.Scheme == "" {
		appLink = "market://launch?id=" + appLinkOrAppID
	}
	return p.sendLaunchApp(appLink)
}

// StartVoice starts a streaming voice session.
//
// A VoiceStream session wrapper is returned if the voice session can be
// established within the given timeout (0 uses the default of 2 seconds). The
// session needs to be closed with End() before a new session is started.
//
// Returns ConnectionClosed if the client is disconnected and an error if the
// device does not begin voice in time or a voice session is already in
// progress.
func (r *AndroidTVRemote) StartVoice(ctx context.Context, timeout time.Duration) (*VoiceStream, error) {
	p, err := r.connectedProtocol("StartVoice")
	if err != nil {
		return nil, err
	}
	sessionID, err := p.startVoice(ctx, timeout)
	if err != nil {
		return nil, err
	}
	return &VoiceStream{proto: p, sessionID: sessionID}, nil
}
