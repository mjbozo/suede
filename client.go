package suede

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
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
	host         string
	path         string
	onConnect    func()
	onDisconnect func()
	onMessage    func([]byte)
	connection   net.Conn
	active       atomic.Bool
}

func WebSocket(rawURL string) (*wsclient, error) {
	urlObject, urlErr := url.Parse(rawURL)
	if urlErr != nil {
		debug.Printf("Error creating URL object: %s\n", urlErr.Error())
		return nil, urlErr
	}

	if urlObject.Path == "" {
		urlObject.Path = "/"
	}

	wsClient := &wsclient{
		host: urlObject.Host,
		path: urlObject.Path,
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
	connectionErr := wsClient.handleConnection()
	if connectionErr != nil {
		return connectionErr
	}

	if wsClient.onConnect != nil {
		wsClient.onConnect()
	}

	clientErrors := make(chan error)
	readBuffer := make([]byte, 2)
	go func() {
		clientErrors <- wsClient.readFromConnection(readBuffer)
	}()

	wsClient.active.Store(true)

	select {
	case <-ctx.Done():
		// graceful shutdown
		debug.Println("Client context done")

	case e := <-clientErrors:
		// error occured
		debug.Printf("Client error channel triggered: %s\n", e)
	}

	closeErr := wsClient.Close()
	if closeErr != nil {
		return closeErr
	}

	if wsClient.onDisconnect != nil {
		wsClient.onDisconnect()
	}

	return nil
}

func (wsClient *wsclient) IsActive() bool {
	active := wsClient.active.Load()
	return active
}

func (wsClient *wsclient) handleConnection() error {
	conn, connErr := net.Dial("tcp", wsClient.host)
	if connErr != nil {
		debug.Printf("Error connecting to %s, terminating connection.\n", wsClient.host)
		if conn != nil {
			conn.Close()
		}
		return connErr
	}

	wsClient.connection = conn

	wsKey := generateWSKey()
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

		case strings.HasPrefix(line, "Sec-WebSocket-Accept"):
			headerValue := strings.Split(line, ": ")[1]
			if strings.TrimSpace(headerValue) != string(wsAccept) {
				debug.Printf("Invalid WS Key.\nExpected: %s\nReceived: %s\n",
					wsAccept, headerValue)
				return &WSClientError{message: "Server responded with invalid WebSocket key"}
			}
		}
	}

	return nil
}

func (wsClient *wsclient) readFromConnection(readBuffer []byte) error {
	for {
		bytesRead, readErr := wsClient.connection.Read(readBuffer)
		if readErr != nil {
			debug.Printf("Read Error: %s\n", readErr.Error())
			return &WSClientError{message: "Read error occurred"}
		}

		if bytesRead != 2 {
			debug.Println("Invalid frame count")
			return nil
		}

		controlByte := readBuffer[0]
		opCode := controlByte & 0b00001111

		switch opCode {
		case OP_CLOSE_CONN:
			codeBuffer := make([]byte, 2)
			wsClient.connection.Read(codeBuffer)
			code := binary.BigEndian.Uint16(codeBuffer)
			debug.Printf("Close code: %d\n", code)
			closeData := make([]byte, 2)
			binary.BigEndian.PutUint16(closeData, uint16(CLOSE_STATUS_NORMAL))
			wsClient.send(FINAL_FRAGMENT|OP_CLOSE_CONN, closeData)
			wsClient.active.Store(false)
			return &WSClientError{message: "Connection closed"}

		case OP_PING:
			debug.Println("got a ping, sending pong")
			wsClient.pong()
			continue

		case OP_PONG:
			debug.Println("got a pong")
			continue
		}

		payloadInfoByte := readBuffer[1]
		mask := payloadInfoByte & 0b10000000
		if mask > 0 {
			debug.Println("Server should not set mask bit")
			return &WSClientError{message: "Server set mask bit"}
		}

		payloadLength := payloadInfoByte & 0b01111111

		var data []byte
		switch {
		case payloadLength < 126:
			data = wsClient.readFrameData(uint64(payloadLength))

		case payloadLength == 126:
			headerSize := 2
			frameBuffer := make([]byte, headerSize)
			bytesRead, err := wsClient.connection.Read(frameBuffer)
			if bytesRead != headerSize || err != nil {
				debug.Println("Failed to read complete payload. Read %d/%d bytes. Error: %s\n", bytesRead, headerSize, err)
				return nil
			}
			payloadLength16 := binary.BigEndian.Uint16(frameBuffer)
			data = wsClient.readFrameData(uint64(payloadLength16))

		case payloadLength == 127:
			headerSize := 8
			frameBuffer := make([]byte, headerSize)
			bytesRead, err := wsClient.connection.Read(frameBuffer)
			if bytesRead != headerSize || err != nil {
				debug.Println("Failed to read complete payload. Read %d/%d bytes. Error: %s\n", bytesRead, headerSize, err)
				return nil
			}
			payloadLength64 := binary.BigEndian.Uint64(frameBuffer)
			data = wsClient.readFrameData(uint64(payloadLength64))
		}

		if wsClient.onMessage != nil {
			wsClient.onMessage(data)
		}
	}
}

func (wsClient *wsclient) readFrameData(length uint64) []byte {
	readBuffer := make([]byte, length)

	bytesRead, err := wsClient.connection.Read(readBuffer)
	if bytesRead != int(length) || err != nil {
		debug.Println("Failed to read complete payload. Read %d/%d bytes. Error: %s\n", bytesRead, length, err)
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
	if !wsClient.active.Load() {
		return nil
	}

	wsClient.active.Store(false)
	wsClient.connection.SetReadDeadline(time.Now())

	controlByte := FINAL_FRAGMENT | OP_CLOSE_CONN

	closeMessage := []byte("Client closed connection")
	payload := make([]byte, len(closeMessage)+2)
	binary.BigEndian.PutUint16(payload, uint16(CLOSE_STATUS_NORMAL))
	copy(payload[2:], closeMessage)

	wsClient.send(controlByte, payload)

	// wait for server close confirmation
	// TODO: Messages may already be in-transit; no guarantee next read frame will be reply to sent close frame
	// Need to probably continue reading until close frame is received, or the timeout occurs
	closeBuf := make([]byte, 2)
	wsClient.connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := wsClient.connection.Read(closeBuf)
	if n != len(closeBuf) || err != nil {
		debug.Printf("Error closing connection safely. %d/%d bytes read. Error: %s\n", n, len(closeBuf), err)
		// return &WSServerError{message: msg}
	}

	if closeBuf[0] != FINAL_FRAGMENT|OP_CLOSE_CONN {
		debug.Printf("Error: expected close response, got: %x\n", closeBuf)
		debug.Println("Closing connection anyway")
	}

	if err = wsClient.connection.Close(); err != nil {
		return err
	}

	return nil
}

func (wsClient *wsclient) Ping() {
	controlByte := FINAL_FRAGMENT | OP_PING
	wsClient.send(controlByte, nil)
}

func (wsClient *wsclient) pong() {
	// TODO: Read payload size in case application data passed with ping
	// Application data must be sent back to the server
	controlByte := FINAL_FRAGMENT | OP_PONG
	wsClient.send(controlByte, nil)
}

func generateMaskKey() []byte {
	mask := make([]byte, 4)
	rand.Read(mask)
	return mask
}
