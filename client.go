package suede

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
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
	OnConnect    func()
	OnDisconnect func()
	OnMessage    func([]byte)
	connection   net.Conn
}

func WebSocket(rawURL string) (*wsclient, error) {
	urlObject, urlErr := url.Parse(rawURL)
	if urlErr != nil {
		fmt.Printf("Error creating URL object: %s\n", urlErr.Error())
		return nil, urlErr
	}

	wsClient := &wsclient{
		host: urlObject.Host,
		path: urlObject.Path,
	}

	return wsClient, nil
}

// Connect initiates the WebSocket handshake with a WebSocket server. Once connected successfully
// a new goroutine will be created which will read from the connection continuously, and return
// control to the caller. A sync.WaitGroup must be managed by the caller. If wg.Wait() is not
// called in the calling function, the WebSocket client will disconnect immediately.
//
// If the caller does not need to regain control, consider calling Run or RuCallback instead.
func (wsClient *wsclient) Connect(wg *sync.WaitGroup) error {
	connectionErr := wsClient.handleConnection()
	if connectionErr != nil {
		return connectionErr
	}

	if wsClient.OnConnect != nil {
		wsClient.OnConnect()
	}

	wg.Add(1)
	go wsClient.readFromConnection(wg)

	return nil
}

// RunCallback initiates the WebSocket handshake with a WebSocker server, and upon successful
// connection, runs the handler function passed as an argument. RunCallback does not return control
// to the caller until the WebSocket client disconnects.
//
// If the caller needs to regain control while the WebSocket client is connected, consider calling
// Connect instead, and managing a sync.WaitGroup manually.
func (wsClient *wsclient) RunCallback(handler func()) error {
	var wg sync.WaitGroup
	connectErr := wsClient.Connect(&wg)
	if connectErr != nil {
		return connectErr
	}

	if handler != nil {
		handler()
	}

	wg.Wait()
	return nil
}

// Run is an alias for RunCallback(nil). It is used to initiate the WebSocket handshake with a
// WebSocket server, but does not execute any callback function. Run does not return control to the
// caller until the WebSocket client disconnects.
//
// If the caller needs to regain control while the WebSocket client is connected, consider calling
// Connect instead, and managing a sync.WaitGroup manually.
func (wsClient *wsclient) Run() error {
	runErr := wsClient.RunCallback(nil)
	return runErr
}

func (wsClient *wsclient) handleConnection() error {
	conn, connErr := net.Dial("tcp", wsClient.host)
	if connErr != nil {
		fmt.Printf("Error connecting to %s, terminating connection.\n", wsClient.host)
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
		fmt.Println(readErr.Error())
		conn.Close()
		return readErr
	}

	responseReader := bytes.NewBuffer(ackBuffer)
	for true {
		line, readStrError := responseReader.ReadString('\n')
		if readStrError != nil {
			if readStrError == io.EOF {
				break
			}

			fmt.Printf("Read Error: %s\n", readStrError.Error())
			conn.Close()
			return readStrError
		}

		switch {
		case strings.HasPrefix(line, "Upgrade"):
			if !strings.HasSuffix(line, "websocket\r\n") {
				fmt.Println("Response not a WebSocket upgrade")
				return &WSClientError{message: "Server response not a WebSocket upgrade"}
			}

		case strings.HasPrefix(line, "Sec-WebSocket-Accept"):
			headerValue := strings.Split(line, ": ")[1]
			if strings.TrimSpace(headerValue) != string(wsAccept) {
				fmt.Printf("Invalid WS Key.\nExpected: %s\nReceived: %s\n",
					wsAccept, headerValue)
				return &WSClientError{message: "Server responded with invalid WebSocket key"}
			}
		}
	}

	return nil
}

func (wsClient *wsclient) readFromConnection(wg *sync.WaitGroup) {
	defer wg.Done()
	defer wsClient.connection.Close()

	if wsClient.OnDisconnect != nil {
		defer wsClient.OnDisconnect()
	}

	readBuffer := make([]byte, 256)

ReadForever:
	for true {
		bytesRead, readErr := wsClient.connection.Read(readBuffer)
		if readErr != nil {
			fmt.Printf("Read Error: %s\n", readErr.Error())
			break ReadForever
		}

		if bytesRead < 2 {
			fmt.Println("Not enough bytes for a frame")
			continue
		}

		controlByte := readBuffer[0]
		opCode := controlByte & 0b00001111
		switch opCode {
		case 0x8:
			break ReadForever

		case 0x9:
			// ping
			fmt.Println("got a ping, sending pong")
			wsClient.pong(wsClient.connection)

		case 0xA:
			// pong
			fmt.Println("got a pong")
			continue

		default:
		}

		payloadInfoByte := readBuffer[1]
		mask := payloadInfoByte & 0b10000000
		if mask > 0 {
			fmt.Println("Server should not set mask bit")
			break
		}

		payloadLength := payloadInfoByte & 0b01111111

		data := make([]byte, payloadLength)
		switch {
		case payloadLength < 126:
			data = wsClient.readFrameData(readBuffer[2:], uint64(payloadLength))

		case payloadLength == 126:
			sizeBytes := []byte{readBuffer[2], readBuffer[3]}
			payloadLength16 := binary.BigEndian.Uint16(sizeBytes)
			data = wsClient.readFrameData(readBuffer[4:], uint64(payloadLength16))

		case payloadLength == 127:
			sizeBytes := []byte{
				readBuffer[2], readBuffer[3], readBuffer[4], readBuffer[5],
				readBuffer[6], readBuffer[7], readBuffer[8], readBuffer[9],
			}
			payloadLength64 := binary.BigEndian.Uint64(sizeBytes)
			data = wsClient.readFrameData(readBuffer[10:], uint64(payloadLength64))
		}

		if wsClient.OnMessage != nil {
			wsClient.OnMessage(data)
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
		fmt.Println("Continutation read err")
		fmt.Println(err.Error())
	}

	for i := 0; i < bytesRead; i++ {
		data = append(data, frameBuffer[i])
	}

	return data
}

func (wsClient *wsclient) SendText(data []byte) {
	wsClient.send(data, false)
}

func (wsClient *wsclient) SendBinary(data []byte) {
	wsClient.send(data, true)
}

// Sends bytes to connected WebSocket server
func (wsClient *wsclient) send(data []byte, isBinary bool) {
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
	controlByte := 0x81
	if isBinary {
		controlByte += 0x01
	}
	frame = append(frame, byte(controlByte))

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
		fmt.Printf("Send error: %s, wrote %d/%d bytes\n", err.Error(), n, len(frame))
	}
}

func (wsClient *wsclient) Ping() {
	wsClient.connection.Write([]byte{0x89, 0x80})
}

func (wsClient *wsclient) pong(connection net.Conn) {
	pongPayload := []byte{0x8A, 0x00}
	connection.Write(pongPayload)
}
