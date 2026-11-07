// Package faketv is a fake Android TV for tests. It speaks the v2 remote
// protocol on an API port and a pairing port, accepts only paired client
// certificates on the API port (unless told to accept any), and records the
// commands it receives.
package faketv

import (
	"bufio"
	"bytes"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/stone-age-io/atv-bridge/third_party/atvremote/proto"
	"github.com/stone-age-io/atv-bridge/third_party/atvremote/remote"
)

type TV struct {
	t      *testing.T
	cert   tls.Certificate
	apiLn  net.Listener
	pairLn net.Listener

	mu        sync.Mutex
	acceptAll bool
	paired    map[string]bool // sha256 of client certificate DER
	on        bool
	codeCh    chan string
	conns     map[*conn]bool

	// Received carries every command message a client sends after the
	// connection is set up: key injects, IME edits, app links.
	Received chan *pb.RemoteMessage
}

// New starts a fake TV on two loopback ports. It is turned on and pairs with
// nobody yet.
func New(t *testing.T) *TV {
	t.Helper()
	certPEM, keyPEM, err := remote.GenerateSelfSignedCert("faketv")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	tv := &TV{
		t: t, cert: cert, on: true,
		paired:   map[string]bool{},
		conns:    map[*conn]bool{},
		codeCh:   make(chan string, 1),
		Received: make(chan *pb.RemoteMessage, 64),
	}
	apiCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAnyClientCert,
		// Rejecting here sends a TLS alert, which is how a real TV tells a
		// client it needs to pair.
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 || !tv.isPaired(raw[0]) {
				return errors.New("client certificate not paired")
			}
			return nil
		},
	}
	pairCfg := &tls.Config{Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAnyClientCert}
	if tv.apiLn, err = tls.Listen("tcp", "127.0.0.1:0", apiCfg); err != nil {
		t.Fatal(err)
	}
	if tv.pairLn, err = tls.Listen("tcp", "127.0.0.1:0", pairCfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tv.Close)
	go tv.acceptAPI()
	go tv.acceptPair()
	return tv
}

// Ports returns the API and pairing ports.
func (tv *TV) Ports() (api, pair int) {
	return port(tv.apiLn), port(tv.pairLn)
}

func port(ln net.Listener) int {
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	n, _ := strconv.Atoi(p)
	return n
}

// Close stops listening and drops every connection.
func (tv *TV) Close() {
	_ = tv.apiLn.Close()
	_ = tv.pairLn.Close()
	tv.Drop()
}

// AcceptAll makes the API port accept any client certificate, so a test can
// skip pairing.
func (tv *TV) AcceptAll() {
	tv.mu.Lock()
	tv.acceptAll = true
	tv.mu.Unlock()
}

// Forget unpairs every client and drops their connections, like a factory
// reset.
func (tv *TV) Forget() {
	tv.mu.Lock()
	tv.acceptAll = false
	tv.paired = map[string]bool{}
	tv.mu.Unlock()
	tv.Drop()
}

func (tv *TV) isPaired(der []byte) bool {
	sum := sha256.Sum256(der)
	tv.mu.Lock()
	defer tv.mu.Unlock()
	return tv.acceptAll || tv.paired[hex.EncodeToString(sum[:])]
}

// Code waits for a pairing to start and returns the code the TV shows.
func (tv *TV) Code() string {
	select {
	case c := <-tv.codeCh:
		return c
	case <-time.After(5 * time.Second):
		tv.t.Fatal("no pairing started")
		return ""
	}
}

// Drop closes every API connection, as if the TV went off the network.
func (tv *TV) Drop() {
	tv.mu.Lock()
	conns := tv.conns
	tv.conns = map[*conn]bool{}
	tv.mu.Unlock()
	for c := range conns {
		_ = c.nc.Close()
	}
}

// SetOn changes the power state and tells connected clients.
func (tv *TV) SetOn(on bool) {
	tv.mu.Lock()
	tv.on = on
	tv.mu.Unlock()
	tv.broadcast(&pb.RemoteMessage{RemoteStart: &pb.RemoteStart{Started: on}})
}

// SetApp tells connected clients the app in the foreground changed.
func (tv *TV) SetApp(app string) {
	tv.broadcast(&pb.RemoteMessage{RemoteImeKeyInject: &pb.RemoteImeKeyInject{
		AppInfo: &pb.RemoteAppInfo{AppPackage: app},
	}})
}

// SetVolume tells connected clients the volume changed.
func (tv *TV) SetVolume(level, max uint32, muted bool) {
	tv.broadcast(&pb.RemoteMessage{RemoteSetVolumeLevel: &pb.RemoteSetVolumeLevel{
		VolumeLevel: level, VolumeMax: max, VolumeMuted: muted,
	}})
}

// Connections returns the number of open API connections.
func (tv *TV) Connections() int {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	return len(tv.conns)
}

func (tv *TV) broadcast(msg *pb.RemoteMessage) {
	tv.mu.Lock()
	var conns []*conn
	for c := range tv.conns {
		conns = append(conns, c)
	}
	tv.mu.Unlock()
	for _, c := range conns {
		_ = c.send(msg)
	}
}

// conn frames protobuf messages with a varint length prefix.
type conn struct {
	nc  net.Conn
	r   *bufio.Reader
	wmu sync.Mutex
}

func (c *conn) send(msg proto.Message) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	buf := binary.AppendUvarint(nil, uint64(len(data)))
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.nc.Write(append(buf, data...))
	return err
}

func (c *conn) read(msg proto.Message) error {
	n, err := binary.ReadUvarint(c.r)
	if err != nil {
		return err
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.r, buf); err != nil {
		return err
	}
	return proto.Unmarshal(buf, msg)
}

func (tv *TV) acceptAPI() {
	for {
		nc, err := tv.apiLn.Accept()
		if err != nil {
			return
		}
		go tv.serveAPI(nc.(*tls.Conn))
	}
}

func (tv *TV) serveAPI(nc *tls.Conn) {
	defer nc.Close()
	if err := nc.Handshake(); err != nil {
		return // an unpaired client: the alert has been sent
	}
	c := &conn{nc: nc, r: bufio.NewReader(nc)}
	tv.mu.Lock()
	on := tv.on
	tv.mu.Unlock()

	// The setup exchange every real TV performs, then the power state.
	msg := &pb.RemoteMessage{}
	if c.send(&pb.RemoteMessage{RemoteConfigure: &pb.RemoteConfigure{
		Code1:      639,
		DeviceInfo: &pb.RemoteDeviceInfo{Vendor: "Google", Model: "FakeTV", AppVersion: "1.0"},
	}}) != nil || c.read(msg) != nil {
		return
	}
	if c.send(&pb.RemoteMessage{RemoteSetActive: &pb.RemoteSetActive{Active: 639}}) != nil || c.read(msg) != nil {
		return
	}
	if c.send(&pb.RemoteMessage{RemoteStart: &pb.RemoteStart{Started: on}}) != nil {
		return
	}

	tv.mu.Lock()
	tv.conns[c] = true
	tv.mu.Unlock()
	defer func() {
		tv.mu.Lock()
		delete(tv.conns, c)
		tv.mu.Unlock()
	}()

	// Ping well inside the client's 16s idle timeout.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				_ = c.send(&pb.RemoteMessage{RemotePingRequest: &pb.RemotePingRequest{Val1: 1}})
			}
		}
	}()

	for {
		m := &pb.RemoteMessage{}
		if err := c.read(m); err != nil {
			return
		}
		if m.GetRemotePingResponse() != nil || m.GetRemoteConfigure() != nil || m.GetRemoteSetActive() != nil {
			continue
		}
		select {
		case tv.Received <- m:
		default:
		}
	}
}

func (tv *TV) acceptPair() {
	for {
		nc, err := tv.pairLn.Accept()
		if err != nil {
			return
		}
		go tv.servePair(nc.(*tls.Conn))
	}
}

func ok() *pb.PairingMessage {
	return &pb.PairingMessage{ProtocolVersion: 2, Status: pb.PairingMessage_STATUS_OK}
}

// servePair runs the pairing exchange. Once the client sends a secret that
// matches the code on screen, its certificate is paired.
func (tv *TV) servePair(nc *tls.Conn) {
	defer nc.Close()
	if err := nc.Handshake(); err != nil {
		return
	}
	c := &conn{nc: nc, r: bufio.NewReader(nc)}
	msg := &pb.PairingMessage{}
	if c.read(msg) != nil || msg.GetPairingRequest() == nil {
		return
	}
	reply := ok()
	reply.PairingRequestAck = &pb.PairingRequestAck{}
	if c.send(reply) != nil || c.read(msg) != nil || msg.GetPairingOption() == nil {
		return
	}
	reply = ok()
	reply.PairingOption = &pb.PairingOption{PreferredRole: pb.RoleType_ROLE_TYPE_OUTPUT}
	if c.send(reply) != nil || c.read(msg) != nil || msg.GetPairingConfiguration() == nil {
		return
	}
	reply = ok()
	reply.PairingConfigurationAck = &pb.PairingConfigurationAck{}
	if c.send(reply) != nil {
		return
	}

	// Show a code on screen: the first byte of the secret, then 4 digits.
	clientDER := nc.ConnectionState().PeerCertificates[0].Raw
	clientPub := nc.ConnectionState().PeerCertificates[0].PublicKey.(*rsa.PublicKey)
	serverCert, _ := x509.ParseCertificate(tv.cert.Certificate[0])
	serverPub := serverCert.PublicKey.(*rsa.PublicKey)
	secret := pairingSecret(clientPub, serverPub, "BEEF")
	code := fmt.Sprintf("%02X", secret[0]) + "BEEF"
	select {
	case <-tv.codeCh: // drop a stale code
	default:
	}
	tv.codeCh <- code

	for {
		if c.read(msg) != nil {
			return
		}
		if !bytes.Equal(msg.GetPairingSecret().GetSecret(), secret) {
			_ = c.send(&pb.PairingMessage{ProtocolVersion: 2, Status: pb.PairingMessage_STATUS_BAD_SECRET})
			continue
		}
		sum := sha256.Sum256(clientDER)
		tv.mu.Lock()
		tv.paired[hex.EncodeToString(sum[:])] = true
		tv.mu.Unlock()
		reply = ok()
		reply.PairingSecretAck = &pb.PairingSecretAck{}
		_ = c.send(reply)
		return
	}
}

// pairingSecret is the protocol's hash of both public keys and the last four
// digits of the code.
func pairingSecret(clientPub, serverPub *rsa.PublicKey, last4 string) []byte {
	suffix, _ := hex.DecodeString(last4)
	h := sha256.New()
	h.Write(clientPub.N.Bytes())
	h.Write(big.NewInt(int64(clientPub.E)).Bytes())
	h.Write(serverPub.N.Bytes())
	h.Write(big.NewInt(int64(serverPub.E)).Bytes())
	h.Write(suffix)
	return h.Sum(nil)
}
