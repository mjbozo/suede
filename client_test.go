package suede

import (
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

type mockConnection struct {
	mu           sync.Mutex
	readData     []byte
	readPos      int
	written      []byte
	closed       bool
	readErr      error
	writeErr     error
	deadline     time.Time
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
		return 0, errors.New("Timeout")
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
func (c *mockConnection) SetDeadline(t time.Time) error      { c.deadline = t; return nil }
func (c *mockConnection) SetReadDeadline(t time.Time) error  { c.readDeadline = t; return nil }
func (c *mockConnection) SetWriteDeadline(t time.Time) error { return nil }

func (c *mockConnection) WrittenBytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.written
}

func TestWebsocketClientConstruction(t *testing.T) {
	successCases := []struct {
		Protocol string
		Host     string
		Path     string
	}{
		{Protocol: "http", Host: "localhost:42069", Path: ""},
		{Protocol: "http", Host: "localhost:42069", Path: "/"},
		{Protocol: "http", Host: "localhost:42069", Path: "/chat"},
		{Protocol: "http", Host: "localhost", Path: ""},
		{Protocol: "http", Host: "localhost", Path: "/"},
		{Protocol: "http", Host: "localhost", Path: "/chat"},
		{Protocol: "ws", Host: "localhost:42069", Path: ""},
		{Protocol: "ws", Host: "localhost:42069", Path: "/"},
		{Protocol: "ws", Host: "localhost:42069", Path: "/chat"},
		{Protocol: "ws", Host: "localhost", Path: ""},
		{Protocol: "ws", Host: "localhost", Path: "/"},
		{Protocol: "ws", Host: "localhost", Path: "/chat"},
	}

	for _, testCase := range successCases {
		url := testCase.Protocol + "://" + testCase.Host + testCase.Path
		client, err := WebSocket(url)
		if client == nil || err != nil {
			t.Errorf("Websocket client initialisation failed for URL %s. Expected client construction and nil error, got client = %v, err = %v", url, client, err)
		}

		if client.host != testCase.Host {
			t.Errorf("Websocket client initialisation failed for URL %s. Expected host %s, got %s", url, testCase.Host, client.host)
		}

		if testCase.Path == "" {
			if client.path != "/" {
				t.Errorf("Websocket client initialisation failed for URL %s. Expected path %s, got %s", url, "/", client.path)
			}
		} else {
			if testCase.Path != client.path {
				t.Errorf("Websocket client initialisation failed for URL %s. Expected host %s, got %s", url, testCase.Path, client.path)
			}
		}

		if client.closeSignal == nil {
			t.Error("Websocket client initialisation failed: closeSignal channel was nil")
		}
	}

	failureCases := []struct {
		Url string
	}{
		{Url: "not a url"},
		{Url: "localhost"},
		{Url: "localhost/"},
		{Url: "localhost:1234"},
		{Url: "localhost/chat"},
		{Url: "localhost:1234/chat"},
		{Url: "http"},
		{Url: "http://"},
		{Url: "ws"},
		{Url: "ws://"},
		{Url: "http:/localhost/chat"},
	}

	for _, testCase := range failureCases {
		client, err := WebSocket(testCase.Url)
		if client != nil || err == nil {
			t.Errorf("Websocket client initialisation failed for URL %s. Expected nil client and non-nil error, got client = %v, err = %v", testCase.Url, client, err)
		}
	}
}

func TestMaskBitSetOnSend(t *testing.T) {
	testCases := []struct {
		controlByte byte
		data        []byte
	}{
		{controlByte: FINAL_FRAGMENT | OP_TEXT_FRAME, data: []byte("hello")},
		{controlByte: FINAL_FRAGMENT | OP_BINARY_FRAME, data: []byte{0x01, 0x02, 0x03}},
		{controlByte: OP_CONTINUE_FRAME, data: []byte("continuing...")},
		{controlByte: FINAL_FRAGMENT | OP_PING},
		{controlByte: FINAL_FRAGMENT | OP_PONG},
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN},
	}

	for _, testCase := range testCases {
		conn := newMockConnection(nil)
		client := &wsclient{connection: conn}
		client.send(testCase.controlByte, testCase.data)

		written := conn.WrittenBytes()
		if len(written) < 2 {
			t.Errorf("Expected at least 2 bytes written, only got %d", len(written))
		}

		maskBit := written[1] & 0b10000000
		if maskBit == 0 {
			t.Errorf("Expected mask bit to be set in client payload")
		}
	}
}
