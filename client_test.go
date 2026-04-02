package suede

import (
	"encoding/binary"
	"slices"
	"sync/atomic"
	"testing"
)

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
	testCases := []sendFrameTestCase{
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

func TestMaskKeyLength(t *testing.T) {
	// test both with and without payload
	testCases := []sendFrameTestCase{
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
		if len(written) != len(testCase.data)+6 {
			t.Errorf("Expected 6 byte header, only got %d", len(written)-len(testCase.data))
		}
	}
}

func TestClientSmallPayloadLengthBits(t *testing.T) {
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
		client := &wsclient{connection: conn}
		client.send(testCase.controlByte, testCase.data)

		written := conn.WrittenBytes()
		if written[1]&0b01111111 != byte(len(testCase.data)) {
			t.Errorf("expected length byte %d, got %d", len(testCase.data), written[1])
		}
	}
}

func TestClientMediumPayloadLengthBits(t *testing.T) {
	conn := newMockConnection(nil)
	client := &wsclient{connection: conn}

	payload := make([]byte, 200)
	client.send(FINAL_FRAGMENT|OP_BINARY_FRAME, payload)

	written := conn.WrittenBytes()
	if written[1] != 0xFE {
		t.Errorf("expected 0xFE length indicator, got 0x%X", written[1])
	}

	encodedLength := binary.BigEndian.Uint16(written[2:4])
	if int(encodedLength) != len(payload) {
		t.Errorf("expected encoded length %d, got %d", len(payload), encodedLength)
	}
}

func TestClientLongPayloadLengthBits(t *testing.T) {
	conn := newMockConnection(nil)
	client := &wsclient{connection: conn}

	payload := make([]byte, 69420)
	client.send(FINAL_FRAGMENT|OP_BINARY_FRAME, payload)

	written := conn.WrittenBytes()
	if written[1] != 0xFF {
		t.Errorf("expected 0xFF length indicator, got 0x%X", written[1])
	}

	encodedLength := binary.BigEndian.Uint64(written[2:10])
	if int(encodedLength) != len(payload) {
		t.Errorf("expected encoded length %d, got %d", len(payload), encodedLength)
	}
}

func TestClientPayloadIsMasked(t *testing.T) {
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
		client := &wsclient{connection: conn}
		client.send(testCase.controlByte, testCase.data)

		written := conn.WrittenBytes()

		if len(written) < 2 {
			t.Fatalf("Expected at least 2 bytes written, got %d", len(written))
		}

		// TODO: Assuming small payloads, add medium and large payloads too
		if testCase.data != nil && slices.Equal(written[6:], testCase.data) {
			t.Errorf("Expected payload to be masked but was equal to umasked")
		}
	}
}

func TestSendTextSetsTextOpCode(t *testing.T) {
	testCases := [][]byte{
		[]byte("hello"),
		make([]byte, 200),
		make([]byte, 69420),
		nil,
	}

	for _, msg := range testCases {
		conn := newMockConnection(nil)
		client := &wsclient{connection: conn}
		client.SendText(msg)

		written := conn.WrittenBytes()

		if len(written) < 1 {
			t.Fatalf("Expected at least 1 byte written, got %d", len(written))
		}

		opCode := written[0] & 0b00001111
		if opCode != OP_TEXT_FRAME {
			t.Errorf("Expected control byte to contain OP_TEXT_FRAME, but got %v", opCode)
		}
	}
}

func TestSendTextSetsFinBit(t *testing.T) {
	testCases := [][]byte{
		[]byte("hello"),
		make([]byte, 200),
		make([]byte, 69420),
		nil,
	}

	for _, msg := range testCases {
		conn := newMockConnection(nil)
		client := &wsclient{connection: conn}
		client.SendText(msg)

		written := conn.WrittenBytes()

		if len(written) < 1 {
			t.Fatalf("Expected at least 1 byte written, got %d", len(written))
		}

		fin := written[0] & 0b10000000
		if fin == 0 {
			t.Error("Expected fin bit to be set, but was unset")
		}
	}
}

func TestSendBinarySetsBinaryOpCode(t *testing.T) {
	testCases := [][]byte{
		{0x01, 0x02, 0x03},
		make([]byte, 200),
		make([]byte, 69420),
		nil,
	}

	for _, msg := range testCases {
		conn := newMockConnection(nil)
		client := &wsclient{connection: conn}
		client.SendBinary(msg)

		written := conn.WrittenBytes()

		if len(written) < 1 {
			t.Fatalf("Expected at least 1 byte written, got %d", len(written))
		}

		opCode := written[0] & 0b00001111
		if opCode != OP_BINARY_FRAME {
			t.Errorf("Expected control byte to contain OP_BINARY_FRAME, but got %v", opCode)
		}
	}
}

func TestSendBinarySetsFinBit(t *testing.T) {
	testCases := [][]byte{
		{0x01, 0x02, 0x03},
		make([]byte, 200),
		make([]byte, 69420),
		nil,
	}

	for _, msg := range testCases {
		conn := newMockConnection(nil)
		client := &wsclient{connection: conn}
		client.SendBinary(msg)

		written := conn.WrittenBytes()

		if len(written) < 1 {
			t.Fatalf("Expected at least 1 byte written, got %d", len(written))
		}

		fin := written[0] & 0b10000000
		if fin == 0 {
			t.Error("Expected fin bit to be set, but was unset")
		}
	}
}

func TestNoPanicWhenOnMessageIsNil(t *testing.T) {
	data := []byte("message sent to client")
	frame := buildServerFrame(FINAL_FRAGMENT|OP_TEXT_FRAME, data)
	conn := newMockConnection(frame)
	client := &wsclient{connection: conn, fragments: make([]byte, 0)}

	defer func() {
		if r := recover(); r != nil {
			t.Error("Expected not to panic, but panicked")
		}
	}()

	readBuffer := make([]byte, 2)
	client.readFromConnection(readBuffer)
}

func TestOnMessageCallbackForUnfragmentedFrame(t *testing.T) {
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
		frame := buildServerFrame(testCase.controlByte, testCase.data)
		conn := newMockConnection(frame)
		client := &wsclient{connection: conn, fragments: make([]byte, 0)}

		var received []byte
		client.OnMessage(func(b []byte) {
			received = b
		})

		readBuffer := make([]byte, 2)
		client.readFromConnection(readBuffer)

		if !slices.Equal(received, testCase.data) {
			t.Errorf("Expected data recieved in onMessage: expected %v, got %v", testCase.data, received)
		}
	}
}

func TestOnMessageCallbackForFragmentedFrames(t *testing.T) {
	part1 := []byte("hello,")
	part2 := []byte(" world")
	frame1 := buildServerFrame(OP_TEXT_FRAME, part1)
	frame2 := buildServerFrame(FINAL_FRAGMENT, part2)
	payload := append(frame1, frame2...)
	conn := newMockConnection(payload)
	client := &wsclient{connection: conn, fragments: make([]byte, 0)}

	var received []byte
	client.OnMessage(func(b []byte) {
		received = b
	})

	readBuffer := make([]byte, 2)
	client.readFromConnection(readBuffer)

	if len(received) != 0 {
		t.Errorf("Should not receive data before end of fragmented messages. Got %v", received)
	}

	client.readFromConnection(readBuffer)
	if len(received) == 0 {
		t.Errorf("Should have received data after final fragmented message. Got %v", received)
	}

	expected := append(part1, part2...)
	if !slices.Equal(received, expected) {
		t.Errorf("Expected data recieved in onMessage: expected %v, got %v", expected, received)
	}
}

func TestClientErrorsIfMaskBitSetFromServer(t *testing.T) {
	testCases := [][]byte{
		[]byte{FINAL_FRAGMENT | OP_TEXT_FRAME, 0x85, 'h', 'e', 'l', 'l', 'o'},
		[]byte{FINAL_FRAGMENT | OP_BINARY_FRAME, 0x83, 0x01, 0x02, 0x03},
	}

	for _, frame := range testCases {
		conn := newMockConnection(frame)
		client := &wsclient{connection: conn}

		err := client.readFromConnection(make([]byte, 2))
		if err == nil {
			t.Error("Expected client to error when mask bit set from server")
		}
	}
}

func TestPingSendsPong(t *testing.T) {
	testCases := []sendFrameTestCase{
		{controlByte: FINAL_FRAGMENT | OP_PING},
		{controlByte: FINAL_FRAGMENT | OP_PING, data: []byte("application data")},
	}

	for _, testCase := range testCases {
		frame := buildServerFrame(testCase.controlByte, testCase.data)
		conn := newMockConnection(frame)
		client := &wsclient{connection: conn}

		client.readFromConnection(make([]byte, 2))

		written := conn.WrittenBytes()
		if len(written) < 2 {
			t.Errorf("Expected at least 2 bytes, got %d", len(written))
		}

		opCode := written[0] & 0b00001111
		if opCode != OP_PONG {
			t.Errorf("Expecting OP_PONG response, got %v", opCode)
		}

		payloadLength := written[1] & 0b01111111
		if int(payloadLength) != len(testCase.data) {
			t.Errorf("Expected payload data to be returned in pong. Expected %d bytes, got %d", len(testCase.data), payloadLength)
		}

		// TODO: Check data actually matches, involved umasking client data
	}
}

func TestCloseFrameEchoesStatusCode(t *testing.T) {
	testCases := []sendFrameTestCase{
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN, data: []byte{0x3, 0xE8}},
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN, data: append([]byte{0x3, 0xE8}, []byte("close reason")...)},
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN, data: []byte{0x3, 0xEC}},
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN, data: append([]byte{0x3, 0xEC}, []byte("close reason")...)},
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN, data: []byte{0x3, 0xED}},
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN, data: append([]byte{0x3, 0xED}, []byte("close reason")...)},
	}

	for _, testCase := range testCases {
		frame := buildServerFrame(testCase.controlByte, testCase.data)
		conn := newMockConnection(frame)
		client := &wsclient{connection: conn}

		client.readFromConnection(make([]byte, 2))
		written := conn.WrittenBytes()

		if len(written) < 2 {
			t.Errorf("Expected at least 2 bytes, got %d", len(written))
		}

		opCode := written[0] & 0b00001111
		if opCode != OP_CLOSE_CONN {
			t.Errorf("Expected opCode OP_CLOSE_CONN, got %v", opCode)
		}

		if len(written[6:]) != 2 {
			t.Errorf("Expected close status to be echoed on close. Expected 2 bytes, got %d", len(written[6:]))
		}

		// TODO: Check data actually matches, involved umasking client data
	}
}

func TestCloseFrameReturnsClientError(t *testing.T) {
	testCases := []sendFrameTestCase{
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN, data: []byte{0x3, 0xE8}},
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN, data: append([]byte{0x3, 0xE8}, []byte("close reason")...)},
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN, data: []byte{0x3, 0xEC}},
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN, data: append([]byte{0x3, 0xEC}, []byte("close reason")...)},
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN, data: []byte{0x3, 0xED}},
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN, data: append([]byte{0x3, 0xED}, []byte("close reason")...)},
	}

	for _, testCase := range testCases {
		frame := buildServerFrame(testCase.controlByte, testCase.data)
		conn := newMockConnection(frame)
		client := &wsclient{connection: conn}

		err := client.readFromConnection(make([]byte, 2))
		if err == nil {
			t.Errorf("Expected error after close")
		}
	}
}

func TestCloseFrameSetsActiveFalse(t *testing.T) {
	testCases := []sendFrameTestCase{
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN, data: []byte{0x3, 0xE8}},
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN, data: append([]byte{0x3, 0xE8}, []byte("close reason")...)},
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN, data: []byte{0x3, 0xEC}},
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN, data: append([]byte{0x3, 0xEC}, []byte("close reason")...)},
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN, data: []byte{0x3, 0xED}},
		{controlByte: FINAL_FRAGMENT | OP_CLOSE_CONN, data: append([]byte{0x3, 0xED}, []byte("close reason")...)},
	}

	for _, testCase := range testCases {
		frame := buildServerFrame(testCase.controlByte, testCase.data)
		conn := newMockConnection(frame)
		client := &wsclient{connection: conn}

		client.readFromConnection(make([]byte, 2))

		if client.active.Load() {
			t.Errorf("Expected active flag to be false")
		}
	}
}

func TestParseFrameReadsSmallPayload(t *testing.T) {
	testCases := [][]byte{
		nil,
		[]byte("hello"),
		{0x01, 0x02, 0x03},
	}

	for _, payload := range testCases {
		conn := newMockConnection(payload)
		client := &wsclient{connection: conn}

		frame := client.parseFrame(byte(len(payload)))
		if !slices.Equal(frame, payload) {
			t.Errorf("Incorrect frame data parsed. Expected %v, got %v", payload, frame)
		}
	}
}

func TestParseFrameReadsMediumPayload(t *testing.T) {
	payload := make([]byte, 202)
	binary.BigEndian.PutUint16(payload, 200)
	conn := newMockConnection(payload)
	client := &wsclient{connection: conn}

	frame := client.parseFrame(0x7E)
	if !slices.Equal(frame, payload[2:]) {
		t.Errorf("Incorrect frame data parsed. Expected %v, got %v", payload, frame)
	}
}

func TestParseFrameReadsLargePayload(t *testing.T) {
	payload := make([]byte, 69428)
	binary.BigEndian.PutUint64(payload, 69420)
	conn := newMockConnection(payload)
	client := &wsclient{connection: conn}

	frame := client.parseFrame(0x7F)
	if !slices.Equal(frame, payload[8:]) {
		t.Errorf("Incorrect frame data parsed. Expected %v, got %v", payload, frame)
	}
}

func TestIsActive(t *testing.T) {
	client, err := WebSocket("ws://localhost:42069/ws")
	if err != nil {
		t.Fatalf("Failed to initialise websocket client: %v", err)
	}

	client.active.Store(true)

	if !client.IsActive() {
		t.Errorf("Expected active flag to be set on start: %v", err)
	}
}

func TestHandleCloseIsNoOpWhenInactive(t *testing.T) {
	conn := newMockConnection(nil)
	client := &wsclient{connection: conn}
	client.active.Store(false)
	err := client.Close()

	if err != nil {
		t.Errorf("Expected error to be nil when closing client, got error: %v", err)
	}

	if len(conn.WrittenBytes()) != 0 {
		t.Errorf("Expected nothing to be written to connection, got %v", conn.WrittenBytes())
	}
}

func TestHandleCloseSetsInactive(t *testing.T) {
	conn := newMockConnection(nil)
	client := &wsclient{connection: conn}
	client.active.Store(true)

	go func() {
		client.closeSignal <- nil
	}()

	client.Close()

	if client.IsActive() {
		t.Error("Expected client active flag to be set to false")
	}
}

func TestHandleCloseRepliesWithCloseFrame(t *testing.T) {
	conn := newMockConnection(nil)
	client := &wsclient{connection: conn}
	client.active.Store(true)

	go func() {
		client.closeSignal <- nil
	}()

	client.Close()

	written := conn.WrittenBytes()
	if len(written) < 2 {
		t.Errorf("Expected at least 2 bytes written, got %d", len(written))
	}

	opCode := written[0] & 0b00001111
	if opCode != OP_CLOSE_CONN {
		t.Errorf("Expected to write OP_CLOSE_CONN, but wrote %v", opCode)
	}
}

func TestHandleCloseErrorsWithTimeoutWhenNoCloseSignal(t *testing.T) {
	conn := newMockConnection(nil)
	client := &wsclient{connection: conn, closeTimeout: 0}
	client.active.Store(true)

	err := client.Close()

	if err == nil {
		t.Error("Expected error after timeout on client close")
	}
}

func TestOnConnectCallbackOnConnection(t *testing.T) {
	conn := newMockConnection(nil)
	client := &wsclient{connection: conn}

	var fired atomic.Bool
	client.OnConnect(func() {
		fired.Store(true)
	})

	// simulate a connection being registered and callback fired
	if client.onConnect != nil {
		client.onConnect()
	}

	if !fired.Load() {
		t.Error("Expected onConnect callback to be fired")
	}
}

func TestOnDisconnectCallbackOnDisconnect(t *testing.T) {
	conn := newMockConnection(nil)
	client := &wsclient{connection: conn}

	var fired atomic.Bool
	client.OnDisconnect(func() {
		fired.Store(true)
	})

	// simulate a connection being registered and callback fired
	if client.onDisconnect != nil {
		client.onDisconnect()
	}

	if !fired.Load() {
		t.Error("Expected onDisconnect callback to be fired")
	}
}
