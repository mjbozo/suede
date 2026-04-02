package suede

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
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

func TestServerErrorsWhenReadFrameMaskBitNotSet(t *testing.T) {
	// frame without mask bit
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
		server.onMessage = func(c *ClientConnection, data []byte) {
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
	server.onMessage = func(c *ClientConnection, data []byte) {
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
		{closeStatus: uint16(CLOSE_STATUS_ERROR), reason: "test"},
		{closeStatus: uint16(CLOSE_STATUS_GOING), reason: "test"},
		{closeStatus: uint16(CLOSE_STATUS_NORMAL)},
		{closeStatus: uint16(CLOSE_STATUS_ERROR)},
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

	// TODO: Test cases with large payloads
	testCases := []struct {
		isBinary bool
		payload  []byte
	}{
		{isBinary: false, payload: []byte("broadcast message")},
		{isBinary: false},
		{isBinary: true, payload: []byte{0x01, 0x02, 0x03}},
		{isBinary: true},
	}

	for _, testCase := range testCases {
		if testCase.isBinary {
			server.BroadcastBinary(testCase.payload)
		} else {
			server.BroadcastText(testCase.payload)
		}

		for i, conn := range clientConnections {
			written := conn.WrittenBytes()
			if len(written) != len(testCase.payload)+2 {
				// assuming small payloads
				t.Errorf("client %d received wrong number of bytes. Expected %d, got %d", i, len(testCase.payload)+2, len(written))
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
	server.BroadcastText([]byte{})
	server.BroadcastBinary([]byte{0x01, 0x02, 0x03})
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
	server.OnMessage(func(c *ClientConnection, data []byte) {
		fired.Store(true)
	})

	client := &ClientConnection{ID: "test", connection: newMockConnection(nil)}
	if server.onMessage != nil {
		server.onMessage(client, []byte("hello"))
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
