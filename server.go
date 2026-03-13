package suede

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mjbozo/suede/debug"
)

type WSServerError struct {
	message string
}

func (err *WSServerError) Error() string {
	return err.message
}

type wsserver struct {
	Port         uint16
	Path         string
	onConnect    func(net.Conn)
	onDisconnect func(net.Conn)
	onMessage    func(net.Conn, []byte)
	active       atomic.Bool
	clients      map[string]net.Conn
	clientMutex  sync.Mutex
	server       *http.Server
}

func WebSocketServer(port uint16, path string) (*wsserver, error) {
	wsServer := &wsserver{
		Port:    port,
		Path:    path,
		clients: make(map[string]net.Conn),
	}

	wsServer.active.Store(false)

	return wsServer, nil
}

func (wsServer *wsserver) OnConnect(connectCallback func(net.Conn)) {
	wsServer.onConnect = connectCallback
}

func (wsServer *wsserver) OnDisconnect(disconnectCallback func(net.Conn)) {
	wsServer.onDisconnect = disconnectCallback
}

func (wsServer *wsserver) OnMessage(messageCallback func(net.Conn, []byte)) {
	wsServer.onMessage = messageCallback
}

// Start spins up the WebSocket server, blocking until server context is cancelled, or an unrecoverable error occurs
func (wsServer *wsserver) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc(wsServer.Path, wsServer.handleClientConnection)

	server := http.Server{
		Handler: mux,
		Addr:    ":" + fmt.Sprintf("%d", wsServer.Port),
	}

	wsServer.server = &server

	serverErrors := make(chan error)
	go func() {
		serverErrors <- server.ListenAndServe()
	}()

	wsServer.active.Store(true)

	select {
	case <-ctx.Done():
		debug.Println("Server context cancelled")
	case serverError := <-serverErrors:
		debug.Println(serverError)
	}

	wsServer.Shutdown(ctx)

	return ctx.Err()
}

func (wsServer *wsserver) IsActive() bool {
	active := wsServer.active.Load()
	return active
}

func (wsServer *wsserver) handleClientConnection(res http.ResponseWriter, req *http.Request) {
	clientID, connectionErr := wsServer.handleConnection(res, req)
	if connectionErr != nil {
		panic("Connection failed")
	}

	wsServer.clientMutex.Lock()
	connection := wsServer.clients[clientID]
	wsServer.clientMutex.Unlock()

	readBuffer := make([]byte, 256)
	var connectionError error
	for connectionError == nil {
		connectionError = wsServer.readFromConnection(connection, readBuffer)
	}

	if errors.Is(connectionError, os.ErrDeadlineExceeded) {
		return
	}

	closeErr := connection.Close()
	if closeErr != nil {
		panic("Failed to close connection")
	}

	wsServer.clientMutex.Lock()
	delete(wsServer.clients, clientID)
	wsServer.clientMutex.Unlock()

	if wsServer.onDisconnect != nil {
		wsServer.onDisconnect(connection)
	}

	connection = nil
}

func (wsServer *wsserver) handleConnection(res http.ResponseWriter, req *http.Request) (string, error) {
	if req.Header.Get("Upgrade") != "websocket" {
		return "", &WSServerError{message: "Request header not requesting websocket upgrade"}
	}

	wsKey := req.Header.Get("Sec-WebSocket-Key")
	if wsKey == "" {
		return "", &WSServerError{message: "Empty Sec-WebSocket-Key header value"}
	}

	wsAccept := GenerateWSAccept(wsKey)
	hijacker, ok := res.(http.Hijacker)
	if !ok {
		return "", &WSServerError{message: "ResponseWriter is not a http.Hijacker"}
	}

	connection, _, err := hijacker.Hijack()
	if err != nil {
		return "", &WSServerError{message: "Failed to hijack the connection"}
	}

	wsServer.clientMutex.Lock()
	clientID := generateClientID()
	debug.Printf("Connecting client with ID: %s\n", clientID)
	wsServer.clients[clientID] = connection
	wsServer.clientMutex.Unlock()

	var content []byte
	content = append(content, "HTTP/1.1 101 Switching Protocols\r\n"...)
	content = append(content, "Upgrade: websocket\r\n"...)
	content = append(content, "Connection: Upgrade\r\n"...)
	content = append(content, fmt.Sprintf("Sec-WebSocket-Accept: %s", wsAccept)...)
	content = append(content, "\r\n\r\n"...)
	connection.Write(content)

	if wsServer.onConnect != nil {
		wsServer.onConnect(connection)
	}

	return clientID, nil
}

func (wsServer *wsserver) readFromConnection(connection net.Conn, readBuffer []byte) error {
	bytesRead, readErr := connection.Read(readBuffer)
	if readErr != nil {
		if readErr == io.EOF {
			debug.Println("Client disconnected")
		} else {
			debug.Printf("Read Error: %s\n", readErr.Error())
		}

		return readErr
	}

	if bytesRead < 2 {
		debug.Println("Not enough bytes for a frame")
		return nil
	}

	controlByte := readBuffer[0]
	opCode := controlByte & 0b00001111

	switch opCode {
	case OP_CLOSE_CONN:
		debug.Println("got close request")
		connection.Write([]byte{0x88, 0x02, 0x03, 0xE8})
		return &WSServerError{message: "Connection closed"}

	case OP_PING:
		debug.Println("got a ping, sending a pong")
		wsServer.pong(connection)
		return nil

	case OP_PONG:
		debug.Println("got a pong")
		return nil
	}

	payloadInfoByte := readBuffer[1]
	mask := payloadInfoByte & 0b10000000
	if mask == 0 {
		debug.Println("Client should set mask bit")
		return &WSServerError{message: "Mask bit not set by client"}
	}

	payloadLength := payloadInfoByte & 0b01111111

	data := make([]byte, 0, payloadLength)
	switch {
	case payloadLength < 126:
		maskValue := readBuffer[2:6]
		data = wsServer.readFrameData(connection, maskValue, readBuffer[6:], uint64(payloadLength))

	case payloadLength == 126:
		sizeBytes := readBuffer[2:4]
		maskValue := readBuffer[4:8]
		payloadLength16 := binary.BigEndian.Uint16(sizeBytes)
		data = wsServer.readFrameData(connection, maskValue, readBuffer[8:], uint64(payloadLength16))

	case payloadLength == 127:
		sizeBytes := readBuffer[2:10]
		maskValue := readBuffer[10:14]
		payloadLength64 := binary.BigEndian.Uint64(sizeBytes)
		data = wsServer.readFrameData(connection, maskValue, readBuffer[14:], uint64(payloadLength64))
	}

	if wsServer.onMessage != nil {
		wsServer.onMessage(connection, data)
	}

	return nil
}

func (wsServer *wsserver) readFrameData(connection net.Conn, mask []byte, readBuffer []byte, length uint64) []byte {
	data := make([]byte, 0, length)
	for i := 0; i < len(readBuffer); i++ {
		data = append(data, readBuffer[i]^mask[i%4])
		if uint64(len(data)) == length {
			break
		}
	}

	bytesWritten := uint64(len(data))
	if length <= bytesWritten {
		return data
	}

	bytesRemaining := length - bytesWritten
	frameBuffer := make([]byte, bytesRemaining)
	bytesRead, err := connection.Read(frameBuffer)
	if err != nil {
		debug.Println("Continutation read err")
		debug.Println(err.Error())
	}

	offset := len(data) % 4
	for i := 0; i < bytesRead; i++ {
		unmaskedData := frameBuffer[i] ^ mask[(i+offset)%4]
		data = append(data, unmaskedData)
	}

	return data
}

func (wsServer *wsserver) SendText(clientID string, data []byte) error {
	controlByte := FINAL_FRAGMENT | OP_TEXT_FRAME

	wsServer.clientMutex.Lock()
	connection := wsServer.clients[clientID]
	wsServer.clientMutex.Unlock()

	return wsServer.send(connection, controlByte, data)
}

func (wsServer *wsserver) SendBinary(clientID string, data []byte) error {
	controlByte := FINAL_FRAGMENT | OP_BINARY_FRAME

	wsServer.clientMutex.Lock()
	connection := wsServer.clients[clientID]
	wsServer.clientMutex.Unlock()

	return wsServer.send(connection, controlByte, data)
}

func (wsServer *wsserver) send(connection net.Conn, controlByte byte, data []byte) error {
	if connection == nil {
		return &WSServerError{message: "Client connection is nil"}
	}

	payloadLength := len(data)
	frameLength := payloadLength + 2

	if payloadLength > 125 {
		frameLength += 2
	}

	if payloadLength > (1 << 16) {
		frameLength += 2
	}

	payload := make([]byte, 0, frameLength)
	payload = append(payload, controlByte)

	if payloadLength <= 125 {
		payload = append(payload, byte(payloadLength))
	} else if payloadLength <= (1 << 16) {
		payload = append(payload, 0x7E)
		payload = binary.BigEndian.AppendUint16(payload, uint16(payloadLength))
	} else if payloadLength > (1 << 16) {
		payload = append(payload, 0x7F)
		payload = binary.BigEndian.AppendUint64(payload, uint64(payloadLength))
	}

	payload = append(payload, data...)
	n, err := connection.Write(payload)
	if n != len(payload) || err != nil {
		return &WSServerError{message: "Failed to write to connection"}
	}

	return nil
}

func (wsServer *wsserver) BroadcastText(data []byte) {
	controlByte := FINAL_FRAGMENT | OP_TEXT_FRAME
	wsServer.broadcast(controlByte, data)
}

func (wsServer *wsserver) BroadcastBinary(data []byte) {
	controlByte := FINAL_FRAGMENT | OP_BINARY_FRAME
	wsServer.broadcast(controlByte, data)
}

func (wsServer *wsserver) broadcast(controlByte byte, data []byte) {
	wsServer.clientMutex.Lock()
	defer wsServer.clientMutex.Unlock()

	for _, client := range wsServer.clients {
		if err := wsServer.send(client, controlByte, data); err != nil {
			debug.Println("Failed to broadcast to client")
		}
	}
}

func (wsServer *wsserver) Shutdown(ctx context.Context) error {
	if !wsServer.active.Load() {
		return nil
	}

	wsServer.active.Store(false)
	shutdownErr := make(chan error, len(wsServer.clients))

	wsServer.clientMutex.Lock()
	defer wsServer.clientMutex.Unlock()

	for _, client := range wsServer.clients {
		shutdownErr <- wsServer.closeClient(client)
	}

	wsServer.server.Close()

	return nil
}

func (wsServer *wsserver) closeClient(client net.Conn) error {
	// force normal read goroutine to exit
	debug.Println("closing client")
	client.SetReadDeadline(time.Now())

	controlByte := FINAL_FRAGMENT | OP_CLOSE_CONN

	closeMessage := "Server closed connection"

	payload := make([]byte, len(closeMessage)+2)
	binary.BigEndian.PutUint16(payload, uint16(CLOSE_STATUS_NORMAL))
	copy(payload[2:], []byte(closeMessage))

	wsServer.send(client, controlByte, payload)

	// wait for client close confirmation
	closeBuf := make([]byte, 2)
	client.SetDeadline(time.Now().Add(5 * time.Second))
	n, err := client.Read(closeBuf)
	if n != len(closeBuf) || err != nil {
		return &WSServerError{message: "Error closing client"}
	}

	if closeBuf[0] != FINAL_FRAGMENT|OP_CLOSE_CONN {
		debug.Printf("Error: expected close response, got: %x\n", closeBuf)
		debug.Println("Closing connection anyway")
	}

	if err = client.Close(); err != nil {
		return err
	}

	return nil
}

func (wsServer *wsserver) Ping() {
	wsServer.clientMutex.Lock()
	for _, client := range wsServer.clients {
		client.Write([]byte{FINAL_FRAGMENT | OP_PING, 0x00})
	}
	wsServer.clientMutex.Unlock()
}

func (wsServer *wsserver) pong(connection net.Conn) {
	connection.Write([]byte{FINAL_FRAGMENT | OP_PONG, 0x00})
}

func generateClientID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
