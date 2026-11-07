package remote

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"

	"google.golang.org/protobuf/proto"
)

// Messages are never anywhere near this size; a larger length prefix means
// the stream is corrupt.
const maxMessageSize = 1 << 20

// protobufConn frames protobuf messages over a connection with a varint
// length prefix, matching the wire format used by Android TV.
type protobufConn struct {
	conn    net.Conn
	reader  *bufio.Reader
	writeMu sync.Mutex
}

func newProtobufConn(conn net.Conn) *protobufConn {
	return &protobufConn{conn: conn, reader: bufio.NewReader(conn)}
}

// readRaw reads one varint-length-prefixed message and returns its payload.
func (c *protobufConn) readRaw() ([]byte, error) {
	size, err := binary.ReadUvarint(c.reader)
	if err != nil {
		return nil, err
	}
	if size > maxMessageSize {
		return nil, fmt.Errorf("message too large: %d bytes", size)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(c.reader, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// readMessage reads one message and unmarshals it into msg.
func (c *protobufConn) readMessage(msg proto.Message) error {
	raw, err := c.readRaw()
	if err != nil {
		return err
	}
	return proto.Unmarshal(raw, msg)
}

// sendMessage marshals msg and writes it with a varint length prefix.
// It is safe for concurrent use.
func (c *protobufConn) sendMessage(msg proto.Message) error {
	size := proto.Size(msg)
	buf := binary.AppendUvarint(make([]byte, 0, size+binary.MaxVarintLen32), uint64(size))
	buf, err := proto.MarshalOptions{}.MarshalAppend(buf, msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.conn.Write(buf)
	return err
}

func (c *protobufConn) close() error { return c.conn.Close() }
