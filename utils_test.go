package suede

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

type sendFrameTestCase struct {
	controlByte byte
	data        []byte
}

type mockConnection struct {
	mu           sync.Mutex
	readData     []byte
	readPos      int
	written      []byte
	closed       bool
	readErr      error
	writeErr     error
	readDeadline time.Time
}

func newMockConnection(data []byte) *mockConnection {
	return &mockConnection{readData: data}
}

func (c *mockConnection) Read(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.readErr != nil {
		return 0, c.readErr
	}

	if !c.readDeadline.IsZero() && time.Now().After(c.readDeadline) {
		return 0, fmt.Errorf("Read timeout: deadline is %v, now is %v", c.readDeadline, time.Now())
	}

	if c.readPos >= len(c.readData) {
		return 0, errors.New("Length exceeded")
	}

	n := copy(b, c.readData[c.readPos:])
	c.readPos += n

	return n, nil
}

func (c *mockConnection) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.writeErr != nil {
		return 0, c.writeErr
	}

	c.written = append(c.written, b...)
	return len(b), nil
}

func (c *mockConnection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *mockConnection) LocalAddr() net.Addr                { return &net.TCPAddr{} }
func (c *mockConnection) RemoteAddr() net.Addr               { return &net.TCPAddr{} }
func (c *mockConnection) SetDeadline(t time.Time) error      { c.readDeadline = t; return nil }
func (c *mockConnection) SetReadDeadline(t time.Time) error  { c.readDeadline = t; return nil }
func (c *mockConnection) SetWriteDeadline(t time.Time) error { return nil }

func (c *mockConnection) WrittenBytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.written
}

type mockHijacker struct {
	conn       *mockConnection
	header     http.Header
	statusCode int
	hijackErr  error
}

func newMockHijacker(conn *mockConnection) *mockHijacker {
	return &mockHijacker{
		conn:   conn,
		header: make(http.Header),
	}
}

func (h *mockHijacker) Write(b []byte) (int, error) {
	return h.conn.Write(b)
}

func (h *mockHijacker) WriteHeader(code int) {
	h.statusCode = code
}

func (h *mockHijacker) Header() http.Header {
	return h.header
}

func (h *mockHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h.hijackErr != nil {
		return nil, nil, h.hijackErr
	}

	rw := bufio.NewReadWriter(bufio.NewReader(h.conn), bufio.NewWriter(h.conn))
	return h.conn, rw, nil
}

func buildClientFrame(controlByte byte, payload []byte) []byte {
	frame := make([]byte, 0)
	frame = append(frame, controlByte)

	mask := make([]byte, 4)
	rand.Read(mask)

	payloadLength := len(payload)
	if payloadLength <= 125 {
		frame = append(frame, 0b10000000|byte(payloadLength))
	} else if payloadLength <= 65535 {
		frame = append(frame, 0x8E)
		frame = binary.BigEndian.AppendUint16(frame, uint16(payloadLength))
	} else {
		frame = append(frame, 0x8F)
		frame = binary.BigEndian.AppendUint64(frame, uint64(payloadLength))
	}

	frame = append(frame, mask...)

	for i := range payload {
		frame = append(frame, payload[i]^mask[i%4])
	}

	return frame
}

func buildServerFrame(controlByte byte, payload []byte) []byte {
	frame := make([]byte, 0)
	frame = append(frame, controlByte)

	payloadLength := len(payload)
	if payloadLength <= 125 {
		frame = append(frame, byte(payloadLength))
	} else if payloadLength <= 65535 {
		frame = append(frame, 0x7E)
		frame = binary.BigEndian.AppendUint16(frame, uint16(payloadLength))
	} else {
		frame = append(frame, 0x7F)
		frame = binary.BigEndian.AppendUint64(frame, uint64(payloadLength))
	}

	frame = append(frame, payload...)

	return frame
}

func newMockDialer(conn net.Conn) func(network, address string) (net.Conn, error) {
	return func(network, address string) (net.Conn, error) {
		return conn, nil
	}
}
