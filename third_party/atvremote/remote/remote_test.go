package remote

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	pb "github.com/skeeeon/atv-bridge/third_party/atvremote/proto"
)

// fakeAndroidTV is a TLS server speaking the varint framed protobuf protocol
// on both the api and pairing ports.
type fakeAndroidTV struct {
	t      *testing.T
	apiLn  net.Listener
	pairLn net.Listener
	cert   tls.Certificate
}

func newFakeAndroidTV(t *testing.T) *fakeAndroidTV {
	t.Helper()
	certPEM, keyPEM, err := GenerateSelfSignedCert("faketv")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAnyClientCert,
	}
	apiLn, err := tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	if err != nil {
		t.Fatal(err)
	}
	pairLn, err := tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { apiLn.Close(); pairLn.Close() })
	return &fakeAndroidTV{t: t, apiLn: apiLn, pairLn: pairLn, cert: cert}
}

func (f *fakeAndroidTV) ports() (apiPort, pairPort int) {
	_, apiPortStr, _ := net.SplitHostPort(f.apiLn.Addr().String())
	_, pairPortStr, _ := net.SplitHostPort(f.pairLn.Addr().String())
	apiPort, _ = strconv.Atoi(apiPortStr)
	pairPort, _ = strconv.Atoi(pairPortStr)
	return apiPort, pairPort
}

// serveRemote accepts one api connection and performs the initial handshake:
// remote_configure and remote_set_active exchanges followed by remote_start.
// It returns the server side connection for further scripted exchanges.
func (f *fakeAndroidTV) serveRemote() <-chan *protobufConn {
	ch := make(chan *protobufConn, 1)
	go func() {
		conn, err := f.apiLn.Accept()
		if err != nil {
			return
		}
		c := newProtobufConn(conn)

		// Server sends its configuration; all features supported.
		must(f.t, c.sendMessage(&pb.RemoteMessage{RemoteConfigure: &pb.RemoteConfigure{
			Code1: 639,
			DeviceInfo: &pb.RemoteDeviceInfo{
				Vendor:     "Google",
				Model:      "Chromecast",
				AppVersion: "1.36",
			},
		}}))
		reply := &pb.RemoteMessage{}
		must(f.t, c.readMessage(reply))
		if reply.GetRemoteConfigure() == nil {
			f.t.Error("expected remote_configure reply from client")
		}

		must(f.t, c.sendMessage(&pb.RemoteMessage{RemoteSetActive: &pb.RemoteSetActive{Active: 639}}))
		reply = &pb.RemoteMessage{}
		must(f.t, c.readMessage(reply))
		if reply.GetRemoteSetActive() == nil {
			f.t.Error("expected remote_set_active reply from client")
		}

		must(f.t, c.sendMessage(&pb.RemoteMessage{RemoteStart: &pb.RemoteStart{Started: true}}))
		ch <- c
	}()
	return ch
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Error(err)
	}
}

func newTestRemote(t *testing.T, f *fakeAndroidTV, opts ...Option) *AndroidTVRemote {
	t.Helper()
	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	apiPort, pairPort := f.ports()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	opts = append([]Option{
		WithAPIPort(apiPort),
		WithPairPort(pairPort),
		WithLogger(logger),
	}, opts...)
	r := New("test-client", certFile, keyFile, "127.0.0.1", opts...)
	generated, err := r.GenerateCertIfMissing()
	if err != nil {
		t.Fatal(err)
	}
	if !generated {
		t.Fatal("expected a certificate to be generated")
	}
	t.Cleanup(r.Disconnect)
	return r
}

func TestConnectAndCommands(t *testing.T) {
	f := newFakeAndroidTV(t)
	r := newTestRemote(t, f)

	volumeUpdated := make(chan VolumeInfo, 1)
	r.AddVolumeInfoUpdatedCallback(func(v VolumeInfo) { volumeUpdated <- v })

	serverCh := f.serveRemote()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	server := <-serverCh

	if isOn, ok := r.IsOn(); !ok || !isOn {
		t.Errorf("IsOn = %v, %v; want true, true", isOn, ok)
	}
	info := r.DeviceInfo()
	if info == nil || info.Manufacturer != "Google" || info.Model != "Chromecast" || info.SWVersion != "1.36" {
		t.Errorf("DeviceInfo = %+v", info)
	}

	// Volume update from the server fires the callback.
	must(t, server.sendMessage(&pb.RemoteMessage{RemoteSetVolumeLevel: &pb.RemoteSetVolumeLevel{
		VolumeLevel: 7, VolumeMax: 100, VolumeMuted: false,
	}}))
	select {
	case v := <-volumeUpdated:
		if v.Level != 7 || v.Max != 100 || v.Muted {
			t.Errorf("volume callback = %+v", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for volume callback")
	}

	// Ping is answered with a pong echoing val1.
	must(t, server.sendMessage(&pb.RemoteMessage{RemotePingRequest: &pb.RemotePingRequest{Val1: 42}}))
	msg := &pb.RemoteMessage{}
	must(t, server.readMessage(msg))
	if msg.GetRemotePingResponse().GetVal1() != 42 {
		t.Errorf("expected pong with val1=42, got %v", msg)
	}

	// Key command.
	if err := r.SendKeyCommand("KEYCODE_VOLUME_UP"); err != nil {
		t.Fatal(err)
	}
	msg = &pb.RemoteMessage{}
	must(t, server.readMessage(msg))
	if msg.GetRemoteKeyInject().GetKeyCode() != pb.RemoteKeyCode_KEYCODE_VOLUME_UP ||
		msg.GetRemoteKeyInject().GetDirection() != pb.RemoteDirection_SHORT {
		t.Errorf("unexpected key inject: %v", msg)
	}

	// Key command with direction and without KEYCODE_ prefix.
	if err := r.SendKeyCommandDirection("POWER", "START_LONG"); err != nil {
		t.Fatal(err)
	}
	msg = &pb.RemoteMessage{}
	must(t, server.readMessage(msg))
	if msg.GetRemoteKeyInject().GetKeyCode() != pb.RemoteKeyCode_KEYCODE_POWER ||
		msg.GetRemoteKeyInject().GetDirection() != pb.RemoteDirection_START_LONG {
		t.Errorf("unexpected key inject: %v", msg)
	}

	// text: prefix routes through SendText.
	if err := r.SendKeyCommand("text:hello"); err != nil {
		t.Fatal(err)
	}
	msg = &pb.RemoteMessage{}
	must(t, server.readMessage(msg))
	batch := msg.GetRemoteImeBatchEdit()
	if batch == nil || len(batch.GetEditInfo()) != 1 ||
		batch.GetEditInfo()[0].GetTextFieldStatus().GetValue() != "hello" ||
		batch.GetEditInfo()[0].GetTextFieldStatus().GetStart() != 4 {
		t.Errorf("unexpected ime batch edit: %v", msg)
	}

	// App id gets the market:// prefix; app links stay as is.
	if err := r.SendLaunchAppCommand("com.google.android.youtube.tv"); err != nil {
		t.Fatal(err)
	}
	msg = &pb.RemoteMessage{}
	must(t, server.readMessage(msg))
	if got := msg.GetRemoteAppLinkLaunchRequest().GetAppLink(); got != "market://launch?id=com.google.android.youtube.tv" {
		t.Errorf("unexpected app link: %q", got)
	}
	if err := r.SendLaunchAppCommand("https://www.netflix.com/title/1"); err != nil {
		t.Fatal(err)
	}
	msg = &pb.RemoteMessage{}
	must(t, server.readMessage(msg))
	if got := msg.GetRemoteAppLinkLaunchRequest().GetAppLink(); got != "https://www.netflix.com/title/1" {
		t.Errorf("unexpected app link: %q", got)
	}

	// Current app updates via remote_ime_key_inject.
	must(t, server.sendMessage(&pb.RemoteMessage{RemoteImeKeyInject: &pb.RemoteImeKeyInject{
		AppInfo: &pb.RemoteAppInfo{AppPackage: "com.google.android.youtube.tv"},
	}}))
	deadline := time.Now().Add(2 * time.Second)
	for {
		if app, _ := r.CurrentApp(); app == "com.google.android.youtube.tv" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for current app update")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestVoiceSession(t *testing.T) {
	f := newFakeAndroidTV(t)
	r := newTestRemote(t, f, WithVoice(true))

	serverCh := f.serveRemote()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	server := <-serverCh

	if enabled, ok := r.IsVoiceEnabled(); !ok || !enabled {
		t.Fatalf("IsVoiceEnabled = %v, %v; want true, true", enabled, ok)
	}

	// The server responds to KEYCODE_SEARCH with remote_voice_begin.
	go func() {
		msg := &pb.RemoteMessage{}
		must(t, server.readMessage(msg))
		if msg.GetRemoteKeyInject().GetKeyCode() != pb.RemoteKeyCode_KEYCODE_SEARCH {
			t.Errorf("expected KEYCODE_SEARCH, got %v", msg)
		}
		must(t, server.sendMessage(&pb.RemoteMessage{RemoteVoiceBegin: &pb.RemoteVoiceBegin{
			SessionId: 7, PackageName: "com.google.android.katniss",
		}}))
	}()

	stream, err := r.StartVoice(ctx, 0)
	if err != nil {
		t.Fatalf("StartVoice: %v", err)
	}
	if stream.SessionID() != 7 {
		t.Errorf("SessionID = %d, want 7", stream.SessionID())
	}

	// Client sends remote_voice_begin back.
	msg := &pb.RemoteMessage{}
	must(t, server.readMessage(msg))
	if msg.GetRemoteVoiceBegin().GetSessionId() != 7 {
		t.Errorf("expected voice begin echo, got %v", msg)
	}

	// Small chunks are padded to the 8 KB minimum.
	ok, err := stream.SendChunk([]byte{1, 2, 3})
	if err != nil || !ok {
		t.Fatalf("SendChunk: %v, %v", ok, err)
	}
	msg = &pb.RemoteMessage{}
	must(t, server.readMessage(msg))
	payload := msg.GetRemoteVoicePayload()
	if payload.GetSessionId() != 7 || len(payload.GetSamples()) != voiceChunkMinSize {
		t.Errorf("payload session=%d len=%d, want 7, %d", payload.GetSessionId(), len(payload.GetSamples()), voiceChunkMinSize)
	}

	// Large chunks are split into 20 KB messages.
	big := make([]byte, voiceChunkSize+100)
	if _, err := stream.SendChunk(big); err != nil {
		t.Fatal(err)
	}
	msg = &pb.RemoteMessage{}
	must(t, server.readMessage(msg))
	if len(msg.GetRemoteVoicePayload().GetSamples()) != voiceChunkSize {
		t.Errorf("first split chunk len = %d, want %d", len(msg.GetRemoteVoicePayload().GetSamples()), voiceChunkSize)
	}
	msg = &pb.RemoteMessage{}
	must(t, server.readMessage(msg))
	if len(msg.GetRemoteVoicePayload().GetSamples()) != 100 {
		t.Errorf("second split chunk len = %d, want 100", len(msg.GetRemoteVoicePayload().GetSamples()))
	}

	if err := stream.End(); err != nil {
		t.Fatal(err)
	}
	msg = &pb.RemoteMessage{}
	must(t, server.readMessage(msg))
	if msg.GetRemoteVoiceEnd().GetSessionId() != 7 {
		t.Errorf("expected voice end, got %v", msg)
	}

	// Sending on an ended stream reports closed without error.
	if ok, err := stream.SendChunk([]byte{1}); ok || err != nil {
		t.Errorf("SendChunk after End = %v, %v; want false, nil", ok, err)
	}
}

func TestPairing(t *testing.T) {
	f := newFakeAndroidTV(t)
	r := newTestRemote(t, f)

	codeCh := make(chan string, 1)
	go func() {
		conn, err := f.pairLn.Accept()
		if err != nil {
			return
		}
		tlsConn := conn.(*tls.Conn)
		c := newProtobufConn(conn)

		msg := &pb.PairingMessage{}
		must(t, c.readMessage(msg))
		if msg.GetPairingRequest().GetClientName() != "test-client" {
			t.Errorf("unexpected pairing request: %v", msg)
		}
		must(t, c.sendMessage(&pb.PairingMessage{
			ProtocolVersion: 2, Status: pb.PairingMessage_STATUS_OK,
			PairingRequestAck: &pb.PairingRequestAck{},
		}))

		msg = &pb.PairingMessage{}
		must(t, c.readMessage(msg))
		if msg.GetPairingOption() == nil {
			t.Errorf("expected pairing option, got %v", msg)
		}
		must(t, c.sendMessage(&pb.PairingMessage{
			ProtocolVersion: 2, Status: pb.PairingMessage_STATUS_OK,
			PairingOption: &pb.PairingOption{PreferredRole: pb.RoleType_ROLE_TYPE_OUTPUT},
		}))

		msg = &pb.PairingMessage{}
		must(t, c.readMessage(msg))
		if msg.GetPairingConfiguration() == nil {
			t.Errorf("expected pairing configuration, got %v", msg)
		}
		must(t, c.sendMessage(&pb.PairingMessage{
			ProtocolVersion: 2, Status: pb.PairingMessage_STATUS_OK,
			PairingConfigurationAck: &pb.PairingConfigurationAck{},
		}))

		// Compute the code the TV would display, from both certificates.
		clientPub := tlsConn.ConnectionState().PeerCertificates[0].PublicKey.(*rsa.PublicKey)
		serverCert, err := x509.ParseCertificate(f.cert.Certificate[0])
		if err != nil {
			t.Error(err)
			return
		}
		serverPub := serverCert.PublicKey.(*rsa.PublicKey)
		secret, err := pairingSecret(clientPub, serverPub, "00BEEF")
		if err != nil {
			t.Error(err)
			return
		}
		codeCh <- hex.EncodeToString(secret[:1]) + "BEEF"

		msg = &pb.PairingMessage{}
		must(t, c.readMessage(msg))
		if got := msg.GetPairingSecret().GetSecret(); len(got) == 0 {
			t.Errorf("expected pairing secret, got %v", msg)
		}
		must(t, c.sendMessage(&pb.PairingMessage{
			ProtocolVersion: 2, Status: pb.PairingMessage_STATUS_OK,
			PairingSecretAck: &pb.PairingSecretAck{},
		}))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.StartPairing(ctx); err != nil {
		t.Fatalf("StartPairing: %v", err)
	}

	// Local PIN validation errors keep the session open for retries.
	var invalidAuth *InvalidAuth
	if err := r.FinishPairing("123"); !errors.As(err, &invalidAuth) {
		t.Errorf("FinishPairing(short) = %v, want InvalidAuth", err)
	}
	if err := r.FinishPairing("ZZZZZZ"); !errors.As(err, &invalidAuth) {
		t.Errorf("FinishPairing(non-hex) = %v, want InvalidAuth", err)
	}

	code := <-codeCh
	if err := r.FinishPairing(code); err != nil {
		t.Fatalf("FinishPairing(%s): %v", code, err)
	}
}

func TestConnectInvalidAuth(t *testing.T) {
	// A server that requires a verified client certificate rejects our self
	// signed one after the handshake; Connect must surface InvalidAuth.
	certPEM, keyPEM, err := GenerateSelfSignedCert("faketv")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		// Force the handshake so the client certificate gets verified and
		// rejected with a TLS alert.
		_ = conn.(*tls.Conn).Handshake()
		conn.Close()
	}()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	dir := t.TempDir()
	r := New("test-client", filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"), "127.0.0.1",
		WithAPIPort(port))
	if _, err := r.GenerateCertIfMissing(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = r.Connect(ctx)
	var invalidAuth *InvalidAuth
	if !errors.As(err, &invalidAuth) {
		t.Fatalf("Connect = %v, want InvalidAuth", err)
	}
}

func TestConnectHandshakeNotAnswered(t *testing.T) {
	// A device that accepts the TCP connection but never answers the TLS
	// handshake is unreachable, not unpaired.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
		}
	}()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	dir := t.TempDir()
	r := New("test-client", filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"), "127.0.0.1",
		WithAPIPort(port))
	if _, err := r.GenerateCertIfMissing(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err = r.Connect(ctx)
	var cannotConnect *CannotConnect
	if !errors.As(err, &cannotConnect) {
		t.Fatalf("Connect = %v, want CannotConnect", err)
	}
}

func TestConnectCannotConnect(t *testing.T) {
	dir := t.TempDir()
	r := New("test-client", filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"), "127.0.0.1",
		WithAPIPort(1)) // nothing listens on port 1
	if _, err := r.GenerateCertIfMissing(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := r.Connect(ctx)
	var cannotConnect *CannotConnect
	if !errors.As(err, &cannotConnect) {
		t.Fatalf("Connect = %v, want CannotConnect", err)
	}
}

func TestConnectMissingCert(t *testing.T) {
	dir := t.TempDir()
	r := New("test-client", filepath.Join(dir, "missing.pem"), filepath.Join(dir, "missing-key.pem"), "127.0.0.1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := r.Connect(ctx)
	var invalidAuth *InvalidAuth
	if !errors.As(err, &invalidAuth) {
		t.Fatalf("Connect = %v, want InvalidAuth", err)
	}
}
