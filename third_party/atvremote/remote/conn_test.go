package remote

import (
	"bytes"
	"net"
	"testing"

	pb "github.com/stone-age-io/atv-bridge/third_party/atvremote/proto"
)

// TestProtobufConnRoundTrip verifies varint length framing, including
// messages larger than 127 bytes whose length prefix takes more than one
// byte (the previous implementation assumed a single length byte).
func TestProtobufConnRoundTrip(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	clientConn := newProtobufConn(client)
	serverConn := newProtobufConn(server)

	long := bytes.Repeat([]byte("x"), 5000)
	sent := &pb.RemoteMessage{RemoteAppLinkLaunchRequest: &pb.RemoteAppLinkLaunchRequest{
		AppLink: string(long),
	}}

	done := make(chan error, 1)
	go func() { done <- clientConn.sendMessage(sent) }()

	received := &pb.RemoteMessage{}
	if err := serverConn.readMessage(received); err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	if got := received.GetRemoteAppLinkLaunchRequest().GetAppLink(); got != string(long) {
		t.Fatalf("round trip mismatch: got %d bytes, want %d", len(got), len(long))
	}
}

// TestProtobufConnMultipleMessages verifies several messages back to back on
// the same connection are framed independently.
func TestProtobufConnMultipleMessages(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	clientConn := newProtobufConn(client)
	serverConn := newProtobufConn(server)

	go func() {
		for i := range 3 {
			_ = clientConn.sendMessage(&pb.RemoteMessage{RemotePingRequest: &pb.RemotePingRequest{
				Val1: int32(i + 1),
			}})
		}
	}()

	for i := range 3 {
		received := &pb.RemoteMessage{}
		if err := serverConn.readMessage(received); err != nil {
			t.Fatalf("readMessage %d: %v", i, err)
		}
		if got := received.GetRemotePingRequest().GetVal1(); got != int32(i+1) {
			t.Fatalf("message %d: got val1 %d, want %d", i, got, i+1)
		}
	}
}
