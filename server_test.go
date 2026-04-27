package suede

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestWebSocketServerConstruction(t *testing.T) {
	testCases := []struct {
		port uint16
		path string
	}{
		{port: 42069, path: "/ws"},
		{port: 8080, path: "/"},
		{port: 42069, path: "/ws"},
		{port: 42069, path: "/ws"},
	}

	for _, testCase := range testCases {
		server, err := WebSocketServer(testCase.port, testCase.path)
		if err != nil {
			t.Fatalf("Failed to initialise websocket server: expected no error, got %v", err)
		}

		if server.Port != testCase.port {
			t.Errorf("Failed to initialise websocket server: expected port %d, got %d", testCase.port, server.Port)
		}

		if server.Path != testCase.path {
			t.Errorf("Failed to initialise websocket server: expected path %s, got %s", testCase.path, server.Path)
		}

		if server.clients == nil {
			t.Error("Failed to initialise websocket server: expected clients map to be non-nil")
		}
	}
}

func TestServerIsActive(t *testing.T) {
	server, _ := WebSocketServer(8080, "/ws")
	server.active.Store(true)
	if !server.IsActive() {
		t.Error("expected IsActive() to return true")
	}

	server.active.Store(false)
	if server.IsActive() {
		t.Error("expected IsActive() to return false")
	}
}

func TestServerWithNilConnectionReturnsError(t *testing.T) {
	testCases := []sendFrameTestCase{
		{controlByte: FINAL_FRAGMENT | OP_TEXT_FRAME, data: []byte("hello")},
		{controlByte: FINAL_FRAGMENT | OP_BINARY_FRAME, data: []byte{0x01, 0x02, 0x03}},
		{controlByte: FINAL_FRAGMENT | OP_PING},
		{controlByte: FINAL_FRAGMENT | OP_PONG},
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN},
	}

	server, _ := WebSocketServer(8080, "/ws")
	client := &ClientConnection{connection: nil}
	for _, testCase := range testCases {
		err := server.send(client, testCase.controlByte, testCase.data)
		if err == nil {
			t.Fatal("expected error for nil connection, got nil")
		}
	}
}

func TestServerSmallPayloadLengthBits(t *testing.T) {
	testCases := []sendFrameTestCase{
		{controlByte: FINAL_FRAGMENT | OP_TEXT_FRAME, data: []byte("hello")},
		{controlByte: FINAL_FRAGMENT | OP_TEXT_FRAME},
		{controlByte: FINAL_FRAGMENT | OP_BINARY_FRAME, data: []byte{0x01, 0x02, 0x03}},
		{controlByte: FINAL_FRAGMENT | OP_BINARY_FRAME},
		{controlByte: FINAL_FRAGMENT | OP_PING},
		{controlByte: FINAL_FRAGMENT | OP_PONG},
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN},
	}

	for _, testCase := range testCases {
		conn := newMockConnection(nil)
		client := &ClientConnection{connection: conn, closeSignal: make(chan *struct{})}
		server, _ := WebSocketServer(8080, "/ws")
		err := server.send(client, testCase.controlByte, testCase.data)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		written := conn.WrittenBytes()
		if written[1] != byte(len(testCase.data)) {
			t.Errorf("expected length byte %d, got %d", len(testCase.data), written[1])
		}
	}
}

func TestServerMediumPayloadLengthBits(t *testing.T) {
	conn := newMockConnection(nil)
	server, _ := WebSocketServer(8080, "/ws")
	client := &ClientConnection{connection: conn, closeSignal: make(chan *struct{})}

	payload := make([]byte, 200)
	server.send(client, FINAL_FRAGMENT|OP_BINARY_FRAME, payload)

	written := conn.WrittenBytes()
	if written[1] != 0x7E {
		t.Errorf("expected 0x7E length indicator, got 0x%X", written[1])
	}

	encodedLength := binary.BigEndian.Uint16(written[2:4])
	if int(encodedLength) != len(payload) {
		t.Errorf("expected encoded length %d, got %d", len(payload), encodedLength)
	}
}

func TestServerLargePayloadLengthBits(t *testing.T) {
	conn := newMockConnection(nil)
	server, _ := WebSocketServer(8080, "/ws")
	client := &ClientConnection{connection: conn, closeSignal: make(chan *struct{})}

	payload := make([]byte, 69420)
	server.send(client, FINAL_FRAGMENT|OP_BINARY_FRAME, payload)

	written := conn.WrittenBytes()
	if written[1] != 0x7F {
		t.Errorf("expected 0x7F length indicator, got 0x%X", written[1])
	}

	encodedLength := binary.BigEndian.Uint64(written[2:10])
	if int(encodedLength) != len(payload) {
		t.Errorf("expected encoded length %d, got %d", len(payload), encodedLength)
	}
}

func TestServerDoesNotSetMaskBit(t *testing.T) {
	testCases := []sendFrameTestCase{
		{controlByte: FINAL_FRAGMENT | OP_TEXT_FRAME, data: []byte("hello")},
		{controlByte: FINAL_FRAGMENT | OP_TEXT_FRAME},
		{controlByte: FINAL_FRAGMENT | OP_BINARY_FRAME, data: []byte{0x01, 0x02, 0x03}},
		{controlByte: FINAL_FRAGMENT | OP_BINARY_FRAME},
		{controlByte: FINAL_FRAGMENT | OP_PING},
		{controlByte: FINAL_FRAGMENT | OP_PONG},
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN},
	}

	conn := newMockConnection(nil)
	server, _ := WebSocketServer(8080, "/ws")
	client := &ClientConnection{connection: conn, closeSignal: make(chan *struct{})}

	for _, testCase := range testCases {
		server.send(client, testCase.controlByte, testCase.data)

		written := conn.WrittenBytes()
		maskBit := written[1] & 0b10000000
		if maskBit != 0 {
			t.Error("Server send error: server should not set mask bit")
		}
	}
}

func TestConcurrentServerSendsAreSafe(t *testing.T) {
	conn := newMockConnection(nil)
	server, _ := WebSocketServer(8080, "/ws")
	client := &ClientConnection{connection: conn, closeSignal: make(chan *struct{})}

	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			server.send(client, FINAL_FRAGMENT|OP_TEXT_FRAME, []byte("hello"))
		}()
	}
	wg.Wait()
}

func TestHandleConnectionErrorsWhenUpgradeHeaderMissing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Add("Sec-WebSocket-Key", generateWSKey())

	res := newMockHijacker(newMockConnection(nil))
	server, _ := WebSocketServer(8080, "/")
	clientID, err := server.handleConnection(res, req)

	if err == nil {
		t.Error("Expected server to error when request missing Upgrade header")
	}

	if clientID != "" {
		t.Errorf("Expected empty clientID when request missing Upgrade header, got %s", clientID)
	}
}

func TestHandleConnectionErrorsWhenNoHijacker(t *testing.T) {
	wsKey := generateWSKey()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Add("Upgrade", "websocket")
	req.Header.Add("Sec-WebSocket-Key", wsKey)
	res := httptest.NewRecorder()

	server, _ := WebSocketServer(8080, "/")
	clientID, err := server.handleConnection(res, req)

	if err == nil {
		t.Error("Expected connection handler to fail at hijack attempt")
	}

	if clientID != "" {
		t.Errorf("Expected empty clientID when hijack should fail")
	}
}

func TestHandleConnectionErrorsWhenHijackFails(t *testing.T) {
	wsKey := generateWSKey()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Add("Upgrade", "websocket")
	req.Header.Add("Sec-WebSocket-Key", wsKey)
	res := newMockHijacker(newMockConnection(nil))
	res.hijackErr = errors.New("Hijack error")

	server, _ := WebSocketServer(8080, "/")
	clientID, err := server.handleConnection(res, req)

	if err == nil {
		t.Error("Expected server to error when hijack fails")
	}

	if clientID != "" {
		t.Errorf("Expected empty clientID when hijack fails")
	}
}

func TestHandleConnectionErrorsOnWriteError(t *testing.T) {
	wsKey := generateWSKey()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Add("Upgrade", "websocket")
	req.Header.Add("Sec-WebSocket-Key", wsKey)

	conn := newMockConnection(nil)
	conn.writeErr = errors.New("Failed to write")
	res := newMockHijacker(conn)

	server, _ := WebSocketServer(8080, "/")
	clientID, err := server.handleConnection(res, req)

	if err == nil {
		t.Error("Expected connection handler to fail on connection write error")
	}

	if clientID != "" {
		t.Errorf("Expected empty clientID when connection write errors")
	}
}

func TestHandleConnectionErrorsWhenWSKeyHeaderMissing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Add("Upgrade", "websocket")
	res := newMockHijacker(newMockConnection(nil))

	server, _ := WebSocketServer(8080, "/")
	clientID, err := server.handleConnection(res, req)

	if err == nil {
		t.Error("Expected server to error when request missing Sec-WebSocket-Key header")
	}

	if clientID != "" {
		t.Errorf("Expected empty clientID when request missing Sec-WebSocket-Key header, got %s", clientID)
	}
}

func TestHandleConnectionAcceptsValidRequest(t *testing.T) {
	wsKey := generateWSKey()
	wsAccept := generateWSAccept(wsKey)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Add("Upgrade", "websocket")
	req.Header.Add("Connection", "Upgrade")
	req.Header.Add("Sec-WebSocket-Key", wsKey)
	res := newMockHijacker(newMockConnection(nil))

	server, _ := WebSocketServer(8080, "/")
	clientID, err := server.handleConnection(res, req)

	if err != nil {
		t.Errorf("Expected no error from valid connection request, got %v", err)
	}

	written := string(res.conn.WrittenBytes())
	if !strings.HasPrefix(written, "HTTP/1.1 101 Switching Protocols") {
		t.Errorf("Expected status 101 from server, got %s", written)
	}

	if !strings.Contains(written, "Upgrade: websocket") {
		t.Errorf("Expected header 'Upgrade: websocket' from server, got %s", written)
	}

	if !strings.Contains(written, "Connection: Upgrade") {
		t.Errorf("Expected header 'Connection: Upgrade' from server, got %s", written)
	}

	if !strings.Contains(written, fmt.Sprintf("Sec-WebSocket-Accept: %s", wsAccept)) {
		t.Errorf("Expected header 'Sec-WebSocket-Accept: %s' from server, got %s", wsAccept, written)
	}

	if clientID == "" {
		t.Error("Expected non-empty clientID from valid connection request")
	}

	server.clientsMutex.Lock()
	if _, ok := server.clients[clientID]; !ok {
		t.Error("Expecting client to be added to server's client map, but was not found")
	}
	server.clientsMutex.Unlock()
}

func TestSuccessfulHandleConnectionCallsOnConnect(t *testing.T) {
	wsKey := generateWSKey()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Add("Upgrade", "websocket")
	req.Header.Add("Connection", "Upgrade")
	req.Header.Add("Sec-WebSocket-Key", wsKey)
	res := newMockHijacker(newMockConnection(nil))

	server, _ := WebSocketServer(8080, "/")
	var fired atomic.Bool
	server.OnConnect(func(cc *ClientConnection) {
		fired.Store(true)
	})

	_, err := server.handleConnection(res, req)

	if err != nil {
		t.Errorf("Expected no error from valid connection request, got %v", err)
	}

	if !fired.Load() {
		t.Error("Expected successful connection handler to call onConnect callback")
	}
}

func TestServerErrorsWhenReadFrameMaskBitNotSet(t *testing.T) {
	frame := []byte{FINAL_FRAGMENT | OP_TEXT_FRAME, 0x05, 'h', 'e', 'l', 'l', 'o'}
	conn := newMockConnection(frame)
	server, _ := WebSocketServer(8080, "/ws")
	client := &ClientConnection{connection: conn, closeSignal: make(chan *struct{})}

	readBuffer := make([]byte, 2)
	err := server.readFromConnection(client, readBuffer)
	if err == nil {
		t.Fatal("Server read error: expected error when mask bit not set, got nil")
	}
}

func TestServerReadFromConnectionCallsOnMessage(t *testing.T) {
	testCases := []sendFrameTestCase{
		{controlByte: FINAL_FRAGMENT | OP_TEXT_FRAME, data: []byte("hello")},
		{controlByte: FINAL_FRAGMENT | OP_TEXT_FRAME},
		{controlByte: FINAL_FRAGMENT | OP_BINARY_FRAME, data: []byte{0x01, 0x02, 0x03}},
		{controlByte: FINAL_FRAGMENT | OP_BINARY_FRAME},
	}

	for _, testCase := range testCases {
		frame := buildClientFrame(testCase.controlByte, testCase.data)

		conn := newMockConnection(frame)
		server, _ := WebSocketServer(8080, "/ws")
		client := &ClientConnection{
			connection:  conn,
			fragments:   make([]byte, 0),
			closeSignal: make(chan *struct{}),
		}

		var received []byte
		server.onMessage = func(c *ClientConnection, data []byte, isBinary bool) {
			received = data
		}

		readBuffer := make([]byte, 2)
		server.readFromConnection(client, readBuffer)

		if !bytes.Equal(received, testCase.data) {
			t.Errorf("Server onMessage callback error: expected captured data to be %q, got %q", testCase.data, received)
		}
	}
}

func TestServerCallsOnMessageAfterFinalFragmentedMessage(t *testing.T) {
	part1 := []byte("hello,")
	part2 := []byte(" world")

	frag1 := buildClientFrame(OP_TEXT_FRAME, part1)
	frag2 := buildClientFrame(FINAL_FRAGMENT, part2)
	frameData := append(frag1, frag2...)

	conn := newMockConnection(frameData)
	server, _ := WebSocketServer(8080, "/ws")
	client := &ClientConnection{
		connection:  conn,
		fragments:   make([]byte, 0),
		closeSignal: make(chan *struct{}),
	}

	var received []byte
	server.onMessage = func(c *ClientConnection, data []byte, isBinary bool) {
		received = data
	}

	readBuffer := make([]byte, 2)
	server.readFromConnection(client, readBuffer)

	if len(received) > 0 {
		t.Error("Server error on fragmented message: expected data not to be received until final fragment")
	}

	server.readFromConnection(client, readBuffer)

	expected := []byte("hello, world")
	if !bytes.Equal(received, expected) {
		t.Errorf("expected %q, got %q", expected, received)
	}
}

func TestServerCloseFrameSendsCloseResponse(t *testing.T) {
	testCases := []struct {
		closeStatus uint16
		reason      string
	}{
		{closeStatus: uint16(CLOSE_STATUS_NORMAL), reason: "test"},
		{closeStatus: uint16(CLOSE_STATUS_PROTOCOL_ERR), reason: "test"},
		{closeStatus: uint16(CLOSE_STATUS_GOING), reason: "test"},
		{closeStatus: uint16(CLOSE_STATUS_NORMAL)},
		{closeStatus: uint16(CLOSE_STATUS_PROTOCOL_ERR)},
		{closeStatus: uint16(CLOSE_STATUS_GOING)},
	}

	for _, testCase := range testCases {
		closePayload := make([]byte, 2)
		binary.BigEndian.PutUint16(closePayload, uint16(CLOSE_STATUS_NORMAL))
		closePayload = append(closePayload, []byte(testCase.reason)...)
		frame := buildClientFrame(FINAL_FRAGMENT|OP_CLOSE_CONN, closePayload)

		conn := newMockConnection(frame)
		server, _ := WebSocketServer(8080, "/ws")
		client := &ClientConnection{
			connection:  conn,
			fragments:   make([]byte, 0),
			closeSignal: make(chan *struct{}),
		}

		readBuffer := make([]byte, 2)
		err := server.readFromConnection(client, readBuffer)

		if err == nil {
			t.Fatal("expected error after close frame, got nil")
		}

		written := conn.WrittenBytes()
		if len(written) < 2 {
			t.Fatal("expected close response to be written")
		}

		opCode := written[0] & 0b00001111
		if opCode != OP_CLOSE_CONN {
			t.Errorf("expected close opcode in response, got %d", opCode)
		}
	}
}

func TestServerSendPongWhenPingReceived(t *testing.T) {
	pingFrame := buildClientFrame(FINAL_FRAGMENT|OP_PING, nil)

	conn := newMockConnection(pingFrame)
	server, _ := WebSocketServer(8080, "/ws")
	client := &ClientConnection{
		connection:  conn,
		fragments:   make([]byte, 0),
		closeSignal: make(chan *struct{}),
	}

	readBuffer := make([]byte, 2)
	server.readFromConnection(client, readBuffer)

	written := conn.WrittenBytes()
	if len(written) < 1 {
		t.Fatal("expected pong to be written")
	}

	opCode := written[0] & 0b00001111
	if opCode != OP_PONG {
		t.Errorf("expected pong opcode, got %d", opCode)
	}
}

func TestServerDoesNotPanicWhenOnMessageNil(t *testing.T) {
	testCases := []sendFrameTestCase{
		{controlByte: FINAL_FRAGMENT | OP_TEXT_FRAME, data: []byte("hello")},
		{controlByte: FINAL_FRAGMENT | OP_TEXT_FRAME},
		{controlByte: FINAL_FRAGMENT | OP_BINARY_FRAME, data: []byte{0x01, 0x02, 0x03}},
		{controlByte: FINAL_FRAGMENT | OP_BINARY_FRAME},
	}

	for _, testCase := range testCases {
		frame := buildClientFrame(testCase.controlByte, testCase.data)
		conn := newMockConnection(frame)
		server, _ := WebSocketServer(8080, "/ws")
		client := &ClientConnection{
			connection:  conn,
			fragments:   make([]byte, 0),
			closeSignal: make(chan *struct{}),
		}

		// server onMessage is nil

		defer func() {
			if r := recover(); r != nil {
				t.Errorf("unexpected panic: %v", r)
			}
		}()

		readBuffer := make([]byte, 2)
		server.readFromConnection(client, readBuffer)
	}
}

func TestParseFrameReturnsFrameData(t *testing.T) {
	testCases := []struct {
		payloadLength byte
		data          []byte
	}{
		{payloadLength: 0x00},
		{payloadLength: 0x05, data: []byte("hello")},
		{payloadLength: 0x7E, data: make([]byte, 200)},
		{payloadLength: 0x7F, data: make([]byte, 69420)},
	}

	for _, testCase := range testCases {
		server, _ := WebSocketServer(8080, "/ws")
		conn := newMockConnection(buildClientFrame(0, testCase.data))
		conn.Read(make([]byte, 2)) // simulate first 2 bytes already being read
		clientConn := &ClientConnection{connection: conn}
		parsed := server.parseFrame(clientConn, testCase.payloadLength)

		if !bytes.Equal(parsed, testCase.data) {
			t.Errorf("Error parsing frame. Expected %v, got %v", testCase.data, parsed)
		}
	}
}

func TestParseFrameReturnsErrorOnReadErr(t *testing.T) {
	testCases := []struct {
		payloadLength byte
		data          []byte
	}{
		{payloadLength: 0x00},
		{payloadLength: 0x05, data: []byte("hello")},
		{payloadLength: 0x7E, data: make([]byte, 200)},
		{payloadLength: 0x7F, data: make([]byte, 69420)},
	}

	for _, testCase := range testCases {
		server, _ := WebSocketServer(8080, "/ws")
		conn := newMockConnection(buildClientFrame(0, testCase.data))
		conn.Read(make([]byte, 2)) // simulate first 2 bytes already being read
		conn.readErr = errors.New("Simulated read error")

		clientConn := &ClientConnection{connection: conn}
		parsed := server.parseFrame(clientConn, testCase.payloadLength)

		if parsed != nil {
			t.Error("Expected nil data returned after read error occurs")
		}
	}
}

func TestServerSendTextSetsCorrectBytes(t *testing.T) {
	testCases := [][]byte{
		[]byte("hello"),
		make([]byte, 200),
		make([]byte, 69420),
		nil,
	}

	for _, msg := range testCases {
		conn := newMockConnection(nil)
		server, _ := WebSocketServer(8080, "/ws")
		client := &ClientConnection{
			connection:  conn,
			fragments:   make([]byte, 0),
			closeSignal: make(chan *struct{}),
		}
		err := server.SendText(client, msg)

		if err != nil {
			t.Errorf("Expected no error when sending text message, got %v", err)
		}

		written := conn.WrittenBytes()

		if len(written) < 1 {
			t.Fatalf("Expected at least 1 byte written, got %d", len(written))
		}

		fin := written[0] & 0b10000000
		if fin == 0 {
			t.Error("Expected fin bit to be set for send text")
		}

		opCode := written[0] & 0b00001111
		if opCode != OP_TEXT_FRAME {
			t.Errorf("Expected control byte to contain OP_TEXT_FRAME, but got %v", opCode)
		}
	}
}

func TestServerSendBinarySetsCorrectBytes(t *testing.T) {
	testCases := [][]byte{
		{0x01, 0x02, 0x03},
		make([]byte, 200),
		make([]byte, 69420),
		nil,
	}

	for _, msg := range testCases {
		conn := newMockConnection(nil)
		server, _ := WebSocketServer(8080, "/ws")
		client := &ClientConnection{
			connection:  conn,
			fragments:   make([]byte, 0),
			closeSignal: make(chan *struct{}),
		}
		err := server.SendBinary(client, msg)

		if err != nil {
			t.Errorf("Expected no error when sending text message, got %v", err)
		}

		written := conn.WrittenBytes()

		if len(written) < 1 {
			t.Fatalf("Expected at least 1 byte written, got %d", len(written))
		}

		fin := written[0] & 0b10000000
		if fin == 0 {
			t.Error("Expected fin bit to be set for send text")
		}

		opCode := written[0] & 0b00001111
		if opCode != OP_BINARY_FRAME {
			t.Errorf("Expected control byte to contain OP_BINARY_FRAME, but got %v", opCode)
		}
	}
}

func TestBroadcastMessageSentToAllClients(t *testing.T) {
	server, _ := WebSocketServer(8080, "/ws")

	clientConnections := make([]*mockConnection, 3)
	for i := range clientConnections {
		clientConnections[i] = newMockConnection(nil)
		id := generateClientID()
		server.clients[id] = &ClientConnection{
			ID:          id,
			connection:  clientConnections[i],
			closeSignal: make(chan *struct{}),
		}
	}

	testCases := []struct {
		isBinary bool
		payload  []byte
		offset   int
	}{
		{isBinary: false, offset: 2},
		{isBinary: false, payload: []byte("broadcast message"), offset: 2},
		{isBinary: false, payload: make([]byte, 200), offset: 4},
		{isBinary: false, payload: make([]byte, 69420), offset: 10},
		{isBinary: true, offset: 2},
		{isBinary: true, payload: []byte{0x01, 0x02, 0x03}, offset: 2},
		{isBinary: true, payload: make([]byte, 200), offset: 4},
		{isBinary: true, payload: make([]byte, 69420), offset: 10},
	}

	for _, testCase := range testCases {
		if testCase.isBinary {
			server.BroadcastBinary(testCase.payload)
		} else {
			server.BroadcastText(testCase.payload)
		}

		for i, conn := range clientConnections {
			written := conn.WrittenBytes()
			if len(written) != len(testCase.payload)+testCase.offset {
				t.Errorf("client %d received wrong number of bytes. Expected %d, got %d", i, len(testCase.payload)+testCase.offset, len(written))
			}

			// reset write buffer for next test
			conn.written = nil
		}
	}
}

func TestBroadcastFailToClientDoesNotErrorOthers(t *testing.T) {
	server, _ := WebSocketServer(8080, "/ws")

	goodConn := newMockConnection(nil)
	badConn := newMockConnection(nil)
	badConn.writeErr = &WSServerError{message: "write failed"}

	goodID := generateClientID()
	badID := generateClientID()

	server.clients[goodID] = &ClientConnection{
		ID: goodID, connection: goodConn, closeSignal: make(chan *struct{}),
	}
	server.clients[badID] = &ClientConnection{
		ID: badID, connection: badConn, closeSignal: make(chan *struct{}),
	}

	// should not panic or block
	payload := []byte("partial success broadcast message")
	server.BroadcastText(payload)

	// assuming small payloads
	if len(goodConn.WrittenBytes()) != len(payload)+2 {
		t.Error("expected good client to receive broadcast despite other client failing")
	}
}

func TestBroadcastToEmptyClientsMapDoesNotPanic(t *testing.T) {
	server, _ := WebSocketServer(8080, "/ws")

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("unexpected panic: %v", r)
		}
	}()

	server.BroadcastText([]byte("hello"))
	server.BroadcastText(make([]byte, 200))
	server.BroadcastText(make([]byte, 69420))
	server.BroadcastText([]byte{})
	server.BroadcastBinary([]byte{0x01, 0x02, 0x03})
	server.BroadcastBinary(make([]byte, 200))
	server.BroadcastBinary(make([]byte, 69420))
	server.BroadcastBinary([]byte{})
}

func TestSnapshotClientsReturnsAllClients(t *testing.T) {
	server, _ := WebSocketServer(8080, "/ws")
	numClients := 5

	for range numClients {
		id := generateClientID()
		server.clients[id] = &ClientConnection{
			ID:          id,
			connection:  newMockConnection(nil),
			closeSignal: make(chan *struct{}),
		}
	}

	snapshot := server.snapshotClients()
	if len(snapshot) != numClients {
		t.Errorf("expected %d clients in snapshot, got %d", numClients, len(snapshot))
	}
}

func TestSnapshotClientsConcurrentAccessIsSafe(t *testing.T) {
	server, _ := WebSocketServer(8080, "/ws")
	numClients := 10

	for range numClients {
		id := generateClientID()
		server.clients[id] = &ClientConnection{
			ID:          id,
			connection:  newMockConnection(nil),
			closeSignal: make(chan *struct{}),
		}
	}

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			server.snapshotClients()
		}()
	}

	wg.Wait()
}

func TestShutdownIsNoOpWhenInactive(t *testing.T) {
	server, _ := WebSocketServer(8080, "/ws")
	server.active.Store(false)

	err := server.Shutdown(context.Background())
	if err != nil {
		t.Errorf("expected nil error for already inactive server, got %v", err)
	}
}

func TestShutdownSetsActiveFalse(t *testing.T) {
	server, _ := WebSocketServer(8080, "/ws")
	server.active.Store(true)
	server.server = &http.Server{}

	err := server.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if server.active.Load() {
		t.Error("expected active to be false after Shutdown")
	}
}

func TestCloseClientSendsCloseFrame(t *testing.T) {
	server, _ := WebSocketServer(8080, "/ws")
	frame := buildClientFrame(FINAL_FRAGMENT|OP_CLOSE_CONN, []byte{0x03, 0xE8})
	conn := newMockConnection(frame)
	client := &ClientConnection{ID: "test", connection: conn, closeSignal: make(chan *struct{})}

	go func() {
		client.closeSignal <- nil
	}()

	payload := "Server closed connection"
	err := server.closeClient(client, CLOSE_STATUS_NORMAL, payload)

	if err != nil {
		t.Errorf("Error closing client from server: %v", err)
	}

	written := conn.WrittenBytes()
	if len(written) != len(payload)+4 {
		t.Errorf("Expected %d bytes written, got %d", len(payload)+4, len(written))
	}

	if written[0] != FINAL_FRAGMENT|OP_CLOSE_CONN {
		t.Errorf("Expected close op code, got %d", written[0])
	}

	if int(written[1]) != len(payload)+2 {
		t.Errorf("Expected payload length byte to be %d, got %d", len(payload)+2, written[1])
	}

	if written[2] != 0x03 || written[3] != 0xE8 {
		t.Errorf("Expected close status 0x3E8, got %x%x", written[2], written[3])
	}
}

func TestCloseClientReadsCloseReply(t *testing.T) {
	server, _ := WebSocketServer(8080, "/ws")
	frame := buildClientFrame(FINAL_FRAGMENT|OP_CLOSE_CONN, []byte{0x03, 0xE8})
	conn := newMockConnection(frame)
	client := &ClientConnection{ID: "test", connection: conn, closeSignal: make(chan *struct{})}

	go func() {
		client.closeSignal <- nil
	}()

	payload := "Server closed connection"
	err := server.closeClient(client, CLOSE_STATUS_NORMAL, payload)

	if err != nil {
		t.Errorf("Error closing client from server: %v", err)
	}

	written := conn.WrittenBytes()
	if len(written) != len(payload)+4 {
		t.Errorf("Expected %d bytes written, got %d", len(payload)+4, len(written))
	}

	if written[0] != FINAL_FRAGMENT|OP_CLOSE_CONN {
		t.Errorf("Expected close op code, got %d", written[0])
	}

	if int(written[1]) != len(payload)+2 {
		t.Errorf("Expected payload length byte to be %d, got %d", len(payload)+2, written[1])
	}

	if written[2] != 0x03 || written[3] != 0xE8 {
		t.Errorf("Expected close status 0x3E8, got %x%x", written[2], written[3])
	}
}

func TestGenerateClientIDIsUnique(t *testing.T) {
	ids := make(map[string]bool)
	numClients := 10000

	for range numClients {
		id := generateClientID()
		if ids[id] {
			t.Errorf("duplicate client ID generated: %s", id)
		}
		ids[id] = true
	}
}

func TestClientIDIsCorrectLength(t *testing.T) {
	id := generateClientID()
	if len(id) != 32 {
		t.Errorf("expected ID length 32, got %d", len(id))
	}
}

func TestOnConnectCallbackFired(t *testing.T) {
	server, _ := WebSocketServer(8080, "/ws")

	var fired atomic.Bool
	server.OnConnect(func(c *ClientConnection) {
		fired.Store(true)
	})

	// simulate a connection being registered and callback fired
	client := &ClientConnection{ID: "test", connection: newMockConnection(nil)}
	if server.onConnect != nil {
		server.onConnect(client)
	}

	if !fired.Load() {
		t.Error("expected onConnect callback to be fired")
	}
}

func TestOnDisconnectCallbackFired(t *testing.T) {
	server, _ := WebSocketServer(8080, "/ws")

	var fired atomic.Bool
	server.OnDisconnect(func(c *ClientConnection) {
		fired.Store(true)
	})

	client := &ClientConnection{ID: "test", connection: newMockConnection(nil)}
	if server.onDisconnect != nil {
		server.onDisconnect(client)
	}

	if !fired.Load() {
		t.Error("expected onDisconnect callback to be fired")
	}
}

func TestOnMessageServerCallbackFired(t *testing.T) {
	server, _ := WebSocketServer(8080, "/ws")

	var fired atomic.Bool
	server.OnMessage(func(c *ClientConnection, data []byte, isBinary bool) {
		fired.Store(true)
	})

	client := &ClientConnection{ID: "test", connection: newMockConnection(nil)}
	if server.onMessage != nil {
		server.onMessage(client, []byte("hello"), false)
	}

	if !fired.Load() {
		t.Error("expected onMessage callback to be fired")
	}
}

func TestPingSendsPingToAllClients(t *testing.T) {
	server, _ := WebSocketServer(8080, "/ws")
	numClients := 10

	conns := make([]*mockConnection, numClients)
	for i := range conns {
		conns[i] = newMockConnection(nil)
		id := generateClientID()
		server.clients[id] = &ClientConnection{
			ID:          id,
			connection:  conns[i],
			closeSignal: make(chan *struct{}),
		}
	}

	server.Ping()

	for i, conn := range conns {
		written := conn.WrittenBytes()
		if len(written) != 2 {
			t.Errorf("client %d received wrong ping bytes", i)
			continue
		}
		opCode := written[0] & 0b00001111
		if opCode != OP_PING {
			t.Errorf("client %d: expected ping opcode, got %d", i, opCode)
		}
	}
}

func TestPongDoesNotError(t *testing.T) {
	server, _ := WebSocketServer(8080, "/ws")
	frame := buildClientFrame(FINAL_FRAGMENT|OP_PONG, nil)
	conn := newMockConnection(frame)
	client := &ClientConnection{ID: "test", connection: conn, closeSignal: make(chan *struct{})}

	err := server.readFromConnection(client, make([]byte, 2))

	if err != nil {
		t.Errorf("Error when server received pong: %v", err)
	}
}

func TestInvalidOpCodesReturnError(t *testing.T) {
	invalidOpCodes := []byte{
		OP_NCTRL_RSVD1, OP_NCTRL_RSVD2, OP_NCTRL_RSVD3, OP_NCTRL_RSVD4,
		OP_CTRL_RSVD1, OP_CTRL_RSVD2, OP_CTRL_RSVD3, OP_CTRL_RSVD4, OP_CTRL_RSVD5,
	}

	for _, opCode := range invalidOpCodes {
		server, _ := WebSocketServer(8080, "/ws")
		conn := newMockConnection(buildClientFrame(opCode, nil))
		client := &ClientConnection{ID: "test", connection: conn, closeSignal: make(chan *struct{})}
		server.active.Store(true)

		err := server.readFromConnection(client, make([]byte, 2))

		if err == nil {
			t.Error("Expected client to return error when invalid op code recieved")
		}
	}
}

func TestClientsCallReturnsAllClients(t *testing.T) {
	server, _ := WebSocketServer(8080, "/ws")

	clientConnections := make([]*mockConnection, 3)
	for i := range clientConnections {
		clientConnections[i] = newMockConnection(nil)
		id := generateClientID()
		server.clients[id] = &ClientConnection{
			ID:          id,
			connection:  clientConnections[i],
			closeSignal: make(chan *struct{}),
		}
	}

	clients := server.Clients()
	if len(clients) != len(clientConnections) {
		t.Errorf("Wrong number of clients returned. Expected %d, got %d", len(clientConnections), len(clients))
	}

	for _, client := range clients {
		if _, ok := server.clients[client.ID]; !ok {
			t.Error("Client not present in server clients map")
		}
	}
}
