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
	readBuffer := make([]byte, 256)
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
		debug.Println("Client error channel triggered")
		debug.Println(e)
		return e
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

	wsKey := GenerateWSKey()
	wsAccept := GenerateWSAccept(wsKey)

	var content []byte
	content = append(content, fmt.Sprintf("GET %s HTTP/1.1\r\n", wsClient.path)...)
	content = append(content, fmt.Sprintf("Host: %s\r\n", wsClient.host)...)
	content = append(content, "Upgrade: websocket\r\n"...)
	content = append(content, "Connection: Upgrade\r\n"...)
	content = append(content, "Sec-WebSocket-Version: 13\r\n"...)
	content = append(content, fmt.Sprintf("Sec-WebSocket-Key: %s", wsKey)...)
	content = append(content, "\r\n\r\n"...)
	conn.Write(content)

	ackBuffer := make([]byte, 256)
	_, readErr := conn.Read(ackBuffer)
	if readErr != nil {
		debug.Println(readErr.Error())
		conn.Close()
		return readErr
	}

	responseReader := bytes.NewBuffer(ackBuffer)
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

		if bytesRead < 2 {
			debug.Println("Not enough bytes for a frame")
			return nil
		}

		controlByte := readBuffer[0]
		opCode := controlByte & 0b00001111

		switch opCode {
		case OP_CLOSE_CONN:
			code := binary.BigEndian.Uint16(readBuffer[2:4])
			debug.Printf("Close code: %d\n", code)
			wsClient.connection.Write([]byte{0x88, 0x02, 0x03, 0xE8})
			return &WSClientError{message: "Connection closed"}

		case OP_PING:
			debug.Println("got a ping, sending pong")
			wsClient.pong(wsClient.connection)
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

		data := make([]byte, payloadLength)
		switch {
		case payloadLength < 126:
			data = wsClient.readFrameData(readBuffer[2:], uint64(payloadLength))

		case payloadLength == 126:
			sizeBytes := readBuffer[2:4]
			payloadLength16 := binary.BigEndian.Uint16(sizeBytes)
			data = wsClient.readFrameData(readBuffer[4:], uint64(payloadLength16))

		case payloadLength == 127:
			sizeBytes := readBuffer[2:10]
			payloadLength64 := binary.BigEndian.Uint64(sizeBytes)
			data = wsClient.readFrameData(readBuffer[10:], uint64(payloadLength64))
		}

		if wsClient.onMessage != nil {
			wsClient.onMessage(data)
		}
	}
}

func (wsClient *wsclient) readFrameData(readBuffer []byte, length uint64) []byte {
	data := make([]byte, 0, length)
	for i := 0; i < len(readBuffer); i++ {
		data = append(data, readBuffer[i])
		if uint64(len(data)) == length {
			break
		}
	}

	if length <= uint64(len(data)) {
		return data
	}

	bytesRemaining := length - uint64(len(data))
	frameBuffer := make([]byte, bytesRemaining)
	bytesRead, err := wsClient.connection.Read(frameBuffer)
	if err != nil {
		debug.Println("Continutation read err")
		debug.Println(err.Error())
	}

	for i := 0; i < bytesRead; i++ {
		data = append(data, frameBuffer[i])
	}

	return data
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
	mask := make([]byte, 4)
	rand.Read(mask)

	maskedData := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		maskedByte := data[i] ^ mask[i%4]
		maskedData = append(maskedData, maskedByte)
	}

	payloadLength := len(maskedData)
	frameLength := payloadLength + 2

	if payloadLength > 125 {
		frameLength += 2
	}

	if payloadLength > (1 << 16) {
		frameLength += 2
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

	// wait for client close confirmation
	closeBuf := make([]byte, 2)
	wsClient.connection.SetDeadline(time.Now().Add(5 * time.Second))
	n, err := wsClient.connection.Read(closeBuf)
	if n != len(closeBuf) || err != nil {
		return &WSServerError{message: "Error closing connection safely"}
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
	wsClient.connection.Write([]byte{FINAL_FRAGMENT | OP_PING, 0x00})
}

func (wsClient *wsclient) pong(connection net.Conn) {
	pongPayload := []byte{FINAL_FRAGMENT | OP_PONG, 0x00}
	connection.Write(pongPayload)
}
