package suede

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mjbozo/suede/debug"
)

type WSClientError struct {
	message string
}

func (err *WSClientError) Error() string {
	return err.message
}

type wsclient struct {
	dial         func(network, address string) (net.Conn, error)
	host         string
	path         string
	onConnect    func()
	onDisconnect func()
	onMessage    func([]byte)
	connection   net.Conn
	active       atomic.Bool
	closeSignal  chan *struct{}
	fragments    []byte
	closeTimeout time.Duration
}

func WebSocket(rawURL string) (*wsclient, error) {
	urlObject, urlErr := url.Parse(rawURL)
	if urlErr != nil {
		debug.Printf("Error creating URL object: %s\n", urlErr.Error())
		return nil, urlErr
	}

	if len(urlObject.Scheme) == 0 || len(urlObject.Host) == 0 {
		debug.Printf("Invalid URL passed to websocket client creation. URL recieved: %s\n", rawURL)
		return nil, &WSClientError{message: "Invalid URL: " + rawURL}
	}

	if urlObject.Path == "" {
		urlObject.Path = "/"
	}

	wsClient := &wsclient{
		dial:         net.Dial,
		host:         urlObject.Host,
		path:         urlObject.Path,
		closeSignal:  make(chan *struct{}),
		fragments:    make([]byte, 0),
		closeTimeout: 5 * time.Second,
	}

	return wsClient, nil
}

func (wsClient *wsclient) OnConnect(connectCallback func()) {
	wsClient.onConnect = connectCallback
}

func (wsClient *wsclient) OnDisconnect(disconnectCallback func()) {
	wsClient.onDisconnect = disconnectCallback
}

func (wsClient *wsclient) OnMessage(messageCallback func([]byte)) {
	wsClient.onMessage = messageCallback
}

// Start initiates the WebSocket handshake with a WebSocket server. Once connected successfully
// a new goroutine will be created which will read from the connection continuously
func (wsClient *wsclient) Start(ctx context.Context) error {
	wsKey := generateWSKey()
	connectionErr := wsClient.handleConnection(wsKey)
	if connectionErr != nil {
		return connectionErr
	}

	if wsClient.onConnect != nil {
		wsClient.onConnect()
	}

	clientErrors := make(chan error)
	go func() {
		readBuffer := make([]byte, 2)
		for {
			clientErr := wsClient.readFromConnection(readBuffer)
			if clientErr != nil {
				clientErrors <- clientErr
			}
		}
	}()

	wsClient.active.Store(true)

	select {
	case <-ctx.Done():
		// graceful shutdown
		debug.Println("Client context done")
		go func() {
			wsClient.closeSignal <- nil
		}()

	case e := <-clientErrors:
		// error occured
		debug.Printf("Client error channel triggered: %s\n", e)
		if errors.Is(e, os.ErrDeadlineExceeded) && !wsClient.active.Load() {
			wsClient.closeSignal <- nil
		}
	}

	closeErr := wsClient.handleClose(CLOSE_STATUS_NORMAL, "Client closed connection")
	if closeErr != nil {
		return closeErr
	}

	if wsClient.onDisconnect != nil {
		wsClient.onDisconnect()
	}

	return ctx.Err()
}

func (wsClient *wsclient) IsActive() bool {
	active := wsClient.active.Load()
	return active
}

func (wsClient *wsclient) handleConnection(wsKey string) error {
	conn, connErr := wsClient.dial("tcp", wsClient.host)
	if connErr != nil {
		debug.Printf("Error connecting to %s, terminating connection.\n", wsClient.host)
		if conn != nil {
			conn.Close()
		}
		return connErr
	}

	wsClient.connection = conn
	wsAccept := generateWSAccept(wsKey)

	var content []byte
	content = append(content, fmt.Sprintf("GET %s HTTP/1.1\r\n", wsClient.path)...)
	content = append(content, fmt.Sprintf("Host: %s\r\n", wsClient.host)...)
	content = append(content, "Upgrade: websocket\r\n"...)
	content = append(content, "Connection: Upgrade\r\n"...)
	content = append(content, "Sec-WebSocket-Version: 13\r\n"...)
	content = append(content, fmt.Sprintf("Sec-WebSocket-Key: %s", wsKey)...)
	content = append(content, "\r\n\r\n"...)
	conn.Write(content)

	ackBuffer := make([]byte, 1)
	ackPayload := make([]byte, 0)

	for {
		_, readErr := conn.Read(ackBuffer)
		if readErr != nil {
			debug.Println(readErr.Error())
			conn.Close()
			return readErr
		}

		ackPayload = append(ackPayload, ackBuffer...)
		ackLength := len(ackPayload)
		if ackLength >= 4 && slices.Equal(ackPayload[ackLength-4:], []byte{'\r', '\n', '\r', '\n'}) {
			break
		}
	}

	responseReader := bytes.NewBuffer(ackPayload)
	upgradeHeaderPresent := false
	secWebsocketAcceptHeaderPresent := false

	for {
		line, readStrError := responseReader.ReadString('\n')
		if readStrError != nil {
			if readStrError == io.EOF {
				break
			}

			debug.Printf("Read Error: %s\n", readStrError.Error())
			conn.Close()
			return readStrError
		}

		switch {
		case strings.HasPrefix(line, "Upgrade"):
			if !strings.HasSuffix(line, "websocket\r\n") {
				debug.Println("Response not a WebSocket upgrade")
				return &WSClientError{message: "Server response not a WebSocket upgrade"}
			}
			upgradeHeaderPresent = true

		case strings.HasPrefix(line, "Sec-WebSocket-Accept"):
			headerValue := strings.Split(line, ": ")[1]
			if strings.TrimSpace(headerValue) != string(wsAccept) {
				debug.Printf("Invalid WS Key.\nExpected: %s\nReceived: %s\n",
					wsAccept, headerValue)
				return &WSClientError{message: "Server responded with invalid WebSocket key"}
			}
			secWebsocketAcceptHeaderPresent = true
		}
	}

	if !upgradeHeaderPresent {
		return &WSClientError{message: "Invalid Upgrade header in response. Requires `Upgrade: websocket` header"}
	}

	if !secWebsocketAcceptHeaderPresent {
		return &WSClientError{message: "Invalid Sec-WebSocket-Accept header in response. Requires header with valid accept key"}
	}

	return nil
}

func (wsClient *wsclient) readFromConnection(readBuffer []byte) error {
	bytesRead, readErr := wsClient.connection.Read(readBuffer)
	if readErr != nil {
		debug.Printf("Read Error: %s\n", readErr.Error())
		return readErr
	}

	if bytesRead != 2 {
		debug.Println("Invalid frame count")
		return nil
	}

	controlByte := readBuffer[0]
	opCode := controlByte & 0b00001111

	payloadInfoByte := readBuffer[1]
	mask := payloadInfoByte & 0b10000000
	if mask != 0 {
		debug.Println("Server should not set mask bit")
		return &WSClientError{message: "Server set mask bit"}
	}

	payloadLength := payloadInfoByte & 0b01111111

	switch opCode {
	case OP_CONTINUE_FRAME, OP_TEXT_FRAME, OP_BINARY_FRAME:
		data := wsClient.parseFrame(payloadLength)
		fin := (controlByte & 0b10000000) != 0

		if fin {
			data = append(wsClient.fragments, data...)
			wsClient.fragments = make([]byte, 0)

			if wsClient.onMessage != nil {
				wsClient.onMessage(data)
			}
		} else {
			wsClient.fragments = append(wsClient.fragments, data...)
		}

	case OP_NCTRL_RSVD1, OP_NCTRL_RSVD2, OP_NCTRL_RSVD3, OP_NCTRL_RSVD4:
		// reserved non-control opcodes - unsupported, close connection
		debug.Println("received reserved non-control opcode, closing connection")
		go func() {
			wsClient.closeSignal <- nil
		}()
		closeMessage := fmt.Sprintf("Invalid opcode received: %d", opCode)
		wsClient.handleClose(CLOSE_STATUS_ERROR, closeMessage)
		return &WSClientError{message: "Received reserved non-control opcode: Connection closed"}

	case OP_CLOSE_CONN:
		var responseCode []byte
		if payloadLength != 0 {
			data := wsClient.parseFrame(payloadLength)
			responseCode = data[0:2]
			closeCode := binary.BigEndian.Uint16(responseCode)
			debug.Printf("Close code: %d, Message: %s\n", closeCode, data[2:])
		}

		wsClient.send(FINAL_FRAGMENT|OP_CLOSE_CONN, responseCode)
		wsClient.active.Store(false)
		return &WSClientError{message: "Connection closed"}

	case OP_PING:
		debug.Println("got a ping, sending pong")
		wsClient.pong(payloadLength)
		return nil

	case OP_PONG:
		debug.Println("got a pong")
		return nil

	case OP_CTRL_RSVD1, OP_CTRL_RSVD2, OP_CTRL_RSVD3, OP_CTRL_RSVD4, OP_CTRL_RSVD5:
		// reserved control opcodes - unsupported, close connection
		debug.Println("received reserved control opcode, closing connection")
		go func() {
			wsClient.closeSignal <- nil
		}()
		closeMessage := fmt.Sprintf("Invalid opcode received: %d", opCode)
		wsClient.handleClose(CLOSE_STATUS_ERROR, closeMessage)
		return &WSServerError{message: "Received reserved control opcode: Connection closed"}
	}

	return nil
}

func (wsClient *wsclient) parseFrame(payloadLength byte) []byte {
	var data []byte
	switch {
	case payloadLength < 126:
		data = wsClient.readFrameData(uint64(payloadLength))

	case payloadLength == 126:
		headerSize := 2
		frameBuffer := make([]byte, headerSize)
		bytesRead, err := wsClient.connection.Read(frameBuffer)
		if bytesRead != headerSize || err != nil {
			debug.Printf("Failed to read complete payload. Read %d/%d bytes. Error: %s\n", bytesRead, headerSize, err)
			return nil
		}
		payloadLength16 := binary.BigEndian.Uint16(frameBuffer)
		data = wsClient.readFrameData(uint64(payloadLength16))

	case payloadLength == 127:
		headerSize := 8
		frameBuffer := make([]byte, headerSize)
		bytesRead, err := wsClient.connection.Read(frameBuffer)
		if bytesRead != headerSize || err != nil {
			debug.Printf("Failed to read complete payload. Read %d/%d bytes. Error: %s\n", bytesRead, headerSize, err)
			return nil
		}
		payloadLength64 := binary.BigEndian.Uint64(frameBuffer)
		data = wsClient.readFrameData(uint64(payloadLength64))
	}

	return data
}

func (wsClient *wsclient) readFrameData(length uint64) []byte {
	readBuffer := make([]byte, length)

	bytesRead, err := wsClient.connection.Read(readBuffer)
	if bytesRead != int(length) || err != nil {
		debug.Printf("Failed to read complete payload. Read %d/%d bytes. Error: %s\n", bytesRead, length, err)
		return nil
	}

	return readBuffer
}

func (wsClient *wsclient) SendText(data []byte) {
	controlByte := FINAL_FRAGMENT | OP_TEXT_FRAME
	wsClient.send(controlByte, data)
}

func (wsClient *wsclient) SendBinary(data []byte) {
	controlByte := FINAL_FRAGMENT | OP_BINARY_FRAME
	wsClient.send(controlByte, data)
}

// Sends bytes to connected WebSocket server
func (wsClient *wsclient) send(controlByte byte, data []byte) {
	mask := generateMaskKey()

	maskedData := make([]byte, 0, len(data))
	for i := range data {
		maskedByte := data[i] ^ mask[i%4]
		maskedData = append(maskedData, maskedByte)
	}

	payloadLength := len(maskedData)
	frameLength := payloadLength + 6 // +2 for control byte and payload byte, +4 for mask bytes

	if payloadLength > 125 {
		frameLength += 2 // extra 2 payload length bytes
	}

	if payloadLength > (1 << 16) {
		frameLength += 4 // another extra 4 payload length bytes
	}

	frame := make([]byte, 0, frameLength)
	frame = append(frame, controlByte)

	if payloadLength <= 125 {
		frame = append(frame, 0b10000000|byte(len(maskedData)))
	} else if payloadLength <= (1 << 16) {
		frame = append(frame, 0xFE)
		frame = binary.BigEndian.AppendUint16(frame, uint16(payloadLength))
	} else if payloadLength > (1 << 16) {
		frame = append(frame, 0xFF)
		frame = binary.BigEndian.AppendUint64(frame, uint64(payloadLength))
	}

	frame = append(frame, mask...)
	frame = append(frame, maskedData...)

	n, err := wsClient.connection.Write(frame)
	if n != len(frame) || err != nil {
		debug.Printf("Send error: %s, wrote %d/%d bytes\n", err.Error(), n, len(frame))
	}
}

func (wsClient *wsclient) Close() error {
	return wsClient.handleClose(CLOSE_STATUS_NORMAL, "Client closed connection")
}

func (wsClient *wsclient) handleClose(closeStatus uint, closeMessage string) error {
	if !wsClient.active.Load() {
		return nil
	}

	wsClient.active.Store(false)
	wsClient.connection.SetReadDeadline(time.Now())

	closeSignalReceived := false
	closeCtx, closeCancel := context.WithTimeout(context.Background(), wsClient.closeTimeout)
	defer closeCancel()

	select {
	case <-wsClient.closeSignal:
		closeSignalReceived = true
	case <-closeCtx.Done():
	}

	controlByte := FINAL_FRAGMENT | OP_CLOSE_CONN

	payload := make([]byte, len(closeMessage)+2)
	binary.BigEndian.PutUint16(payload, uint16(closeStatus))
	copy(payload[2:], closeMessage)

	wsClient.send(controlByte, payload)

	if !closeSignalReceived {
		return &WSClientError{message: "Failed to wait on close signal. Close frame sent to server, but not waiting for response."}
	}

	// wait for server close confirmation
	wsClient.connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	closeBuf := make([]byte, 2)

	readCtx, readCancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer readCancel()

	for closeBuf[0] != FINAL_FRAGMENT|OP_CLOSE_CONN && readCtx.Err() == nil {
		n, err := wsClient.connection.Read(closeBuf)
		if !errors.Is(err, os.ErrDeadlineExceeded) && (n != len(closeBuf) || err != nil) {
			debug.Printf("Error closing connection safely. %d/%d bytes read. Error: %s\n", n, len(closeBuf), err)
			errMsg := fmt.Sprintf("Error reading close frame from server: %v", err)
			return &WSClientError{message: errMsg}
		}
		// NOTE: potentially handle in-transit messages here. For now, they are effectively being dropped
	}

	if closeBuf[0] == FINAL_FRAGMENT|OP_CLOSE_CONN {
		debug.Println("Received close response")
		payloadLength := closeBuf[1] & 0b01111111
		if payloadLength != 0 {
			data := wsClient.parseFrame(payloadLength)
			closeCode := binary.BigEndian.Uint16(data)
			debug.Printf("Returned close code: %d\n", closeCode)
		}
	} else {
		// NOTE: This path is hard to test
		// Needs to continuously flood connection so read does not return 0 bytes and trigger error path
		debug.Printf("Error: expected close response, but did not receive before timeout\nClosing connection anyway...\n")
	}

	if err := wsClient.connection.Close(); err != nil {
		return err
	}

	return nil
}

func (wsClient *wsclient) Ping() {
	controlByte := FINAL_FRAGMENT | OP_PING
	wsClient.send(controlByte, nil)
}

func (wsClient *wsclient) pong(payloadLength byte) {
	pingData := wsClient.parseFrame(payloadLength)
	controlByte := FINAL_FRAGMENT | OP_PONG
	wsClient.send(controlByte, pingData)
}

func generateMaskKey() []byte {
	mask := make([]byte, 4)
	rand.Read(mask)
	return mask
}
