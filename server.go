package suede

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
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
	clients      []net.Conn
	clientMutex  sync.Mutex
	server       *http.Server
}

func WebSocketServer(port uint16, path string) (*wsserver, error) {
	wsServer := &wsserver{
		Port: port,
		Path: path,
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
		fmt.Println("Server context cancelled")
	case serverError := <-serverErrors:
		fmt.Println(serverError)
	}

	wsServer.Shutdown(ctx)

	return ctx.Err()
}

func (wsServer *wsserver) IsActive() bool {
	active := wsServer.active.Load()
	return active
}

func (wsServer *wsserver) Clients() []net.Conn {
	var clients []net.Conn

	wsServer.clientMutex.Lock()
	clients = wsServer.clients
	wsServer.clientMutex.Unlock()

	return clients
}

func (wsServer *wsserver) handleClientConnection(res http.ResponseWriter, req *http.Request) {
	connection, connectionErr := wsServer.handleConnection(res, req)
	if connectionErr != nil {
		panic("Connection failed")
	}

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
	for i := range wsServer.clients {
		if wsServer.clients[i] == connection {
			wsServer.clients = append(wsServer.clients[:i], wsServer.clients[i+1:]...)
			break
		}
	}
	wsServer.clientMutex.Unlock()

	if wsServer.onDisconnect != nil {
		wsServer.onDisconnect(connection)
	}

	connection = nil
}

func (wsServer *wsserver) handleConnection(res http.ResponseWriter, req *http.Request) (net.Conn, error) {
	if req.Header.Get("Upgrade") != "websocket" {
		return nil, &WSServerError{message: "Request header not requesting websocket upgrade"}
	}

	wsKey := req.Header.Get("Sec-WebSocket-Key")
	if wsKey == "" {
		return nil, &WSServerError{message: "Empty Sec-WebSocket-Key header value"}
	}

	wsAccept := GenerateWSAccept(wsKey)
	hijacker, ok := res.(http.Hijacker)
	if !ok {
		return nil, &WSServerError{message: "ResponseWriter is not a http.Hijacker"}
	}

	connection, _, err := hijacker.Hijack()
	if err != nil {
		return nil, &WSServerError{message: "Failed to hijack the connection"}
	}

	wsServer.clientMutex.Lock()
	wsServer.clients = append(wsServer.clients, connection)
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

	return connection, nil
}

func (wsServer *wsserver) readFromConnection(connection net.Conn, readBuffer []byte) error {
	bytesRead, readErr := connection.Read(readBuffer)
	if readErr != nil {
		if readErr == io.EOF {
			fmt.Println("Client disconnected")
		} else {
			fmt.Printf("Read Error: %s\n", readErr.Error())
		}

		return readErr
	}

	if bytesRead < 2 {
		fmt.Println("Not enough bytes for a frame")
		return nil
	}

	controlByte := readBuffer[0]
	opCode := controlByte & 0b00001111
	switch opCode {
	case 0x8:
		// close
		connection.Write([]byte{0x88, 0x02, 0x03, 0xE8})
		return &WSServerError{message: "Connection closed"}

	case 0x9:
		// ping
		fmt.Println("got a ping, sending a pong")
		wsServer.pong(connection)
		return nil

	case 0xA:
		// pong
		fmt.Println("got a pong")
		return nil
	}

	payloadInfoByte := readBuffer[1]
	mask := payloadInfoByte & 0b10000000
	if mask == 0 {
		fmt.Println("Client should set mask bit")
		return &WSServerError{message: "Mask bit not set by client"}
	}

	payloadLength := payloadInfoByte & 0b01111111

	data := make([]byte, 0, payloadLength)
	switch {
	case payloadLength < 126:
		maskValue := readBuffer[2:6]
		data = wsServer.readFrameData(connection, maskValue, readBuffer[6:], uint64(payloadLength))

	case payloadLength == 126:
		sizeBytes := []byte{readBuffer[2], readBuffer[3]}
		maskValue := readBuffer[4:8]
		payloadLength16 := binary.BigEndian.Uint16(sizeBytes)
		data = wsServer.readFrameData(connection, maskValue, readBuffer[8:], uint64(payloadLength16))

	case payloadLength == 127:
		sizeBytes := []byte{
			readBuffer[2], readBuffer[3], readBuffer[4], readBuffer[5],
			readBuffer[6], readBuffer[7], readBuffer[8], readBuffer[9],
		}
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
		fmt.Println("Continutation read err")
		fmt.Println(err.Error())
	}

	offset := len(data) % 4
	for i := 0; i < bytesRead; i++ {
		unmaskedData := frameBuffer[i] ^ mask[(i+offset)%4]
		data = append(data, unmaskedData)
	}

	return data
}

func (wsServer *wsserver) SendText(connection net.Conn, data []byte) {
	wsServer.send(connection, data, false)
}

func (wsServer *wsserver) SendBinary(connection net.Conn, data []byte) {
	wsServer.send(connection, data, true)
}

func (wsServer *wsserver) send(connection net.Conn, data []byte, isBinary bool) {
	payloadLength := len(data)
	frameLength := payloadLength + 2

	if payloadLength > 125 {
		frameLength += 2
	}

	if payloadLength > (1 << 16) {
		frameLength += 2
	}

	responsePayload := make([]byte, 0, frameLength)

	controlByte := 0x81
	if isBinary {
		controlByte += 0x01
	}
	responsePayload = append(responsePayload, byte(controlByte))

	if payloadLength <= 125 {
		responsePayload = append(responsePayload, byte(payloadLength))
	} else if payloadLength <= (1 << 16) {
		responsePayload = append(responsePayload, 0x7E)
		responsePayload = binary.BigEndian.AppendUint16(responsePayload, uint16(payloadLength))
	} else if payloadLength > (1 << 16) {
		responsePayload = append(responsePayload, 0x7F)
		responsePayload = binary.BigEndian.AppendUint64(responsePayload, uint64(payloadLength))
	}

	responsePayload = append(responsePayload, data...)
	n, err := connection.Write(responsePayload)
	if n != len(responsePayload) || err != nil {
		fmt.Println(err)
	}
}

func (wsServer *wsserver) BroadcastText(data []byte) {
	wsServer.broadcast(data, false)
}

func (wsServer *wsserver) BroadcastBinary(data []byte) {
	wsServer.broadcast(data, true)
}

func (wsServer *wsserver) broadcast(data []byte, isBinary bool) {
	wsServer.clientMutex.Lock()
	defer wsServer.clientMutex.Unlock()

	for _, client := range wsServer.clients {
		wsServer.send(client, data, isBinary)
	}
}

func (wsServer *wsserver) Shutdown(ctx context.Context) error {
	if !wsServer.active.Load() {
		return nil
	}

	wsServer.active.Store(false)
	shutdownErr := make(chan error, 1)
	wsServer.Close(shutdownErr)
	wsServer.server.Close()
	return nil
}

func (wsServer *wsserver) Close(shutdownErr chan<- error) {
	fmt.Println("Attempting graceful shutdown")
	wsServer.clientMutex.Lock()
	defer wsServer.clientMutex.Unlock()

	for _, client := range wsServer.clients {
		shutdownErr <- wsServer.closeClient(client)
	}
}

func (wsServer *wsserver) closeClient(client net.Conn) error {
	// force normal read goroutine to exit
	client.SetReadDeadline(time.Now())

	payload := []byte("Server closed connection")
	payloadSize := len(payload) + 2

	data := make([]byte, 0)
	data = append(data, 0x88)
	data = append(data, byte(payloadSize))
	data = append(data, []byte{0x03, 0xE8}...) // 1000 - Normal Closure
	data = append(data, payload...)
	client.Write(data)

	// wait for client close confirmation
	closeBuf := make([]byte, 2)
	client.SetDeadline(time.Now().Add(5 * time.Second))
	n, err := client.Read(closeBuf)
	if n != len(closeBuf) || err != nil {
		return &WSServerError{message: "Error closing client"}
	}

	if closeBuf[0] != 0x88 {
		fmt.Printf("Error: expected close response, got: %x\n", closeBuf)
		fmt.Println("Closing connection anyway")
	}

	if err = client.Close(); err != nil {
		return err
	}

	return nil
}

func (wsServer *wsserver) Ping() {
	for _, client := range wsServer.clients {
		client.Write([]byte{0x89, 0x00})
	}
}

func (wsServer *wsserver) pong(connection net.Conn) {
	connection.Write([]byte{0x8A, 0x00})
}
