package remote

import (
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/big"

	pb "github.com/skeeeon/atv-bridge/third_party/atvremote/proto"
)

// pairingSession implements the pairing protocol with an Android TV.
//
// Messages transmitted between client and server are of type PairingMessage,
// see pairingmessage.proto. Protocol is described in
// https://github.com/Aymkdn/assistant-freebox-cloud/wiki/Google-TV-(aka-Android-TV)-Remote-Control-(v2)
type pairingSession struct {
	conn       *protobufConn
	tlsConn    *tls.Conn
	clientName string
	clientCert *tls.Certificate
	logger     *slog.Logger
}

func newPairingMessage() *pb.PairingMessage {
	return &pb.PairingMessage{
		ProtocolVersion: 2,
		Status:          pb.PairingMessage_STATUS_OK,
	}
}

// roundTrip sends msg and reads the server's reply, checking its status.
func (s *pairingSession) roundTrip(msg *pb.PairingMessage) (*pb.PairingMessage, error) {
	logDebugProto(s.logger, "Sending", msg)
	if err := s.conn.sendMessage(msg); err != nil {
		return nil, &ConnectionClosed{Message: "connection has been lost", Err: err}
	}
	resp := &pb.PairingMessage{}
	if err := s.conn.readMessage(resp); err != nil {
		return nil, &ConnectionClosed{Message: "connection has been lost", Err: err}
	}
	logDebugProto(s.logger, "Received", resp)
	if resp.GetStatus() != pb.PairingMessage_STATUS_OK {
		if resp.GetStatus() == pb.PairingMessage_STATUS_BAD_SECRET {
			return nil, &InvalidAuth{Message: "invalid pairing secret"}
		}
		return nil, &ConnectionClosed{Message: fmt.Sprintf("received status: %s", resp.GetStatus())}
	}
	return resp, nil
}

// start performs the first phase of the pairing process, after which the
// Android TV shows the pairing code on screen.
func (s *pairingSession) start() error {
	// ----------------- Pairing Request --------------------
	msg := newPairingMessage()
	msg.PairingRequest = &pb.PairingRequest{
		ClientName:  s.clientName,
		ServiceName: "atvremote",
	}
	resp, err := s.roundTrip(msg)
	if err != nil {
		return err
	}
	if resp.GetPairingRequestAck() == nil {
		return &ConnectionClosed{Message: "expected pairing request ack"}
	}

	// ----------------- Options --------------------
	msg = newPairingMessage()
	msg.PairingOption = &pb.PairingOption{
		PreferredRole: pb.RoleType_ROLE_TYPE_INPUT,
		InputEncodings: []*pb.PairingEncoding{{
			Type:         pb.PairingEncoding_ENCODING_TYPE_HEXADECIMAL,
			SymbolLength: 6,
		}},
	}
	resp, err = s.roundTrip(msg)
	if err != nil {
		return err
	}
	if resp.GetPairingOption() == nil {
		return &ConnectionClosed{Message: "expected pairing options"}
	}

	// ----------------- Configuration --------------------
	msg = newPairingMessage()
	msg.PairingConfiguration = &pb.PairingConfiguration{
		ClientRole: pb.RoleType_ROLE_TYPE_INPUT,
		Encoding: &pb.PairingEncoding{
			Type:         pb.PairingEncoding_ENCODING_TYPE_HEXADECIMAL,
			SymbolLength: 6,
		},
	}
	resp, err = s.roundTrip(msg)
	if err != nil {
		return err
	}
	if resp.GetPairingConfigurationAck() == nil {
		return &ConnectionClosed{Message: "expected pairing configuration ack"}
	}
	return nil
}

// finish performs the second phase of the pairing process using the pairing
// code shown on the Android TV.
func (s *pairingSession) finish(pairingCode string) error {
	if len(pairingCode) != 6 {
		s.logger.Debug("Length of PIN should be exactly 6", "pin", pairingCode)
		return &InvalidAuth{Message: "length of PIN should be exactly 6"}
	}
	if _, err := hex.DecodeString(pairingCode); err != nil {
		s.logger.Debug("PIN should be in hex", "pin", pairingCode)
		return &InvalidAuth{Message: "PIN should be in hex", Err: err}
	}

	// tls.LoadX509KeyPair populates Leaf since Go 1.23, but it can be
	// disabled via GODEBUG=x509keypairleaf=0.
	leaf := s.clientCert.Leaf
	if leaf == nil {
		var err error
		if leaf, err = x509.ParseCertificate(s.clientCert.Certificate[0]); err != nil {
			return &InvalidAuth{Message: "client certificate", Err: err}
		}
	}
	clientPub, err := rsaPublicKey(leaf.PublicKey)
	if err != nil {
		return &InvalidAuth{Message: "client certificate", Err: err}
	}
	peerCerts := s.tlsConn.ConnectionState().PeerCertificates
	if len(peerCerts) == 0 {
		return &ConnectionClosed{Message: "no server certificate"}
	}
	serverPub, err := rsaPublicKey(peerCerts[0].PublicKey)
	if err != nil {
		return &InvalidAuth{Message: "server certificate", Err: err}
	}

	secret, err := pairingSecret(clientPub, serverPub, pairingCode)
	if err != nil {
		return &InvalidAuth{Message: "invalid pairing code", Err: err}
	}
	expectedFirstByte, err := hex.DecodeString(pairingCode[0:2])
	if err != nil || len(expectedFirstByte) != 1 {
		return &InvalidAuth{Message: "PIN should be in hex", Err: err}
	}
	if secret[0] != expectedFirstByte[0] {
		s.logger.Debug("Unexpected hash for pairing code", "pin", pairingCode)
		return &InvalidAuth{Message: fmt.Sprintf("unexpected hash for pairing code: %s", pairingCode)}
	}

	// ----------------- Secret --------------------
	msg := newPairingMessage()
	msg.PairingSecret = &pb.PairingSecret{Secret: secret}
	resp, err := s.roundTrip(msg)
	if err != nil {
		return err
	}
	if resp.GetPairingSecretAck() == nil {
		return &ConnectionClosed{Message: "expected pairing secret ack"}
	}
	return nil
}

func (s *pairingSession) close() {
	_ = s.conn.close()
}

func rsaPublicKey(pub any) (*rsa.PublicKey, error) {
	key, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("certificate does not contain an RSA public key (%T)", pub)
	}
	return key, nil
}

// pairingSecret computes the SHA-256 hash of the client and server public
// keys' moduli and exponents plus the last 4 digits of the pairing code. The
// first byte of the hash must match the first 2 digits of the pairing code,
// and the full hash is the secret sent to the server.
func pairingSecret(clientPub, serverPub *rsa.PublicKey, pairingCode string) ([]byte, error) {
	codeBytes, err := hex.DecodeString(pairingCode[2:])
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write(clientPub.N.Bytes())
	h.Write(exponentBytes(clientPub.E))
	h.Write(serverPub.N.Bytes())
	h.Write(exponentBytes(serverPub.E))
	h.Write(codeBytes)
	return h.Sum(nil), nil
}

func exponentBytes(e int) []byte {
	return big.NewInt(int64(e)).Bytes()
}
