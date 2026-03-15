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
	"runtime"
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

type ClientConnection struct {
	id          string
	connection  net.Conn
	closeSignal chan *struct{}
	mu          sync.Mutex
}

type wsserver struct {
	Port         uint16
	Path         string
	onConnect    func(*ClientConnection)
	onDisconnect func(*ClientConnection)
	onMessage    func(*ClientConnection, []byte)
	active       atomic.Bool
	clients      map[string]*ClientConnection
	clientsMutex sync.RWMutex
	server       *http.Server
}

func WebSocketServer(port uint16, path string) (*wsserver, error) {
	wsServer := &wsserver{
		Port:    port,
		Path:    path,
		clients: make(map[string]*ClientConnection),
	}

	return wsServer, nil
}

func (wsServer *wsserver) OnConnect(connectCallback func(*ClientConnection)) {
	wsServer.onConnect = connectCallback
}

func (wsServer *wsserver) OnDisconnect(disconnectCallback func(*ClientConnection)) {
	wsServer.onDisconnect = disconnectCallback
}

func (wsServer *wsserver) OnMessage(messageCallback func(*ClientConnection, []byte)) {
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

	go func() {
		for {
			select {
			case <-time.Tick(10 * time.Second):
				wsServer.Ping()
			case <-ctx.Done():
				return
			}
		}
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
		panic("Failed to establish connection with client: " + connectionErr.Error())
	}

	wsServer.clientsMutex.RLock()
	client := wsServer.clients[clientID]
	wsServer.clientsMutex.RUnlock()

	readBuffer := make([]byte, 2)
	var connectionError error
	for connectionError == nil {
		connectionError = wsServer.readFromConnection(client, readBuffer)
	}

	if errors.Is(connectionError, os.ErrDeadlineExceeded) && !wsServer.active.Load() {
		client.closeSignal <- nil
		return
	}

	closeErr := client.connection.Close()
	if closeErr != nil {
		panic("Failed to close connection")
	}

	wsServer.clientsMutex.Lock()
	delete(wsServer.clients, clientID)
	wsServer.clientsMutex.Unlock()

	if wsServer.onDisconnect != nil {
		wsServer.onDisconnect(client)
	}

	client.connection = nil
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

	hijackedConnection, _, err := hijacker.Hijack()
	if err != nil {
		return "", &WSServerError{message: "Failed to hijack the connection"}
	}

	clientID := generateClientID()
	// debug.Printf("Connecting client with ID: %s\n", clientID)

	var content []byte
	content = append(content, "HTTP/1.1 101 Switching Protocols\r\n"...)
	content = append(content, "Upgrade: websocket\r\n"...)
	content = append(content, "Connection: Upgrade\r\n"...)
	content = append(content, fmt.Sprintf("Sec-WebSocket-Accept: %s", wsAccept)...)
	content = append(content, "\r\n\r\n"...)
	bytesWritten, err := hijackedConnection.Write(content)
	// debug.Printf("Bytes written when acking handshake: %d\n", bytesWritten)

	if bytesWritten != len(content) || err != nil {
		debug.Printf("Failed to write to hijacked connection. Wrote %d/%d bytes. Error: %s\n", bytesWritten, len(content), err)
		return "", &WSServerError{"Failed to write to hijacked connection"}
	}

	wsServer.clientsMutex.Lock()
	clientConnection := &ClientConnection{id: clientID, connection: hijackedConnection, closeSignal: make(chan *struct{})}
	wsServer.clients[clientID] = clientConnection
	wsServer.clientsMutex.Unlock()

	if wsServer.onConnect != nil {
		wsServer.onConnect(clientConnection)
	}

	return clientID, nil
}

func (wsServer *wsserver) readFromConnection(clientConnection *ClientConnection, readBuffer []byte) error {
	connection := clientConnection.connection
	bytesRead, readErr := connection.Read(readBuffer)

	if readErr != nil {
		if readErr == io.EOF {
			debug.Println("Client disconnected")
		} else {
			debug.Printf("Read Error: %s\n", readErr.Error())
		}

		return readErr
	}

	if bytesRead != 2 {
		debug.Println("Invalid frame count")
		return nil
	}

	controlByte := readBuffer[0]
	opCode := controlByte & 0b00001111

	switch opCode {
	case OP_CLOSE_CONN:
		debug.Println("got close request")
		closeData := make([]byte, 2)
		binary.BigEndian.PutUint16(closeData, uint16(CLOSE_STATUS_NORMAL))
		wsServer.send(clientConnection, FINAL_FRAGMENT|OP_CLOSE_CONN, closeData)
		return &WSServerError{message: "Connection closed"}

	case OP_PING:
		debug.Println("got a ping, sending a pong")
		maskBuffer := make([]byte, 4)
		connection.Read(maskBuffer)
		wsServer.pong(clientConnection)
		return nil

	case OP_PONG:
		debug.Printf("got a pong: %v\n", readBuffer)
		maskBuffer := make([]byte, 4)
		connection.Read(maskBuffer)
		return nil
	}

	payloadInfoByte := readBuffer[1]
	mask := payloadInfoByte & 0b10000000
	if mask == 0 {
		debug.Println("Client should set mask bit")
		return &WSServerError{message: "Mask bit not set by client"}
	}

	payloadLength := payloadInfoByte & 0b01111111

	var data []byte
	switch {
	case payloadLength < 126:
		headerSize := 4
		frameBuffer := make([]byte, headerSize)
		bytesRead, err := connection.Read(frameBuffer)
		if bytesRead != headerSize || err != nil {
			debug.Printf("Failed to read complete payload. Read %d/%d bytes. Error: %s\n", bytesRead, headerSize, err)
			return nil
		}
		data = wsServer.readFrameData(connection, frameBuffer, uint64(payloadLength))

	case payloadLength == 126:
		headerSize := 6
		frameBuffer := make([]byte, headerSize)
		bytesRead, err := connection.Read(frameBuffer)
		if bytesRead != headerSize || err != nil {
			debug.Println("Failed to read complete payload. Read %d/%d bytes. Error: %s\n", bytesRead, headerSize, err)
			return nil
		}
		sizeBytes := frameBuffer[:2]
		mask := frameBuffer[2:6]
		payloadLength16 := binary.BigEndian.Uint16(sizeBytes)
		data = wsServer.readFrameData(connection, mask, uint64(payloadLength16))

	case payloadLength == 127:
		headerSize := 12
		frameBuffer := make([]byte, headerSize)
		bytesRead, err := connection.Read(frameBuffer)
		if bytesRead != headerSize || err != nil {
			debug.Println("Failed to read complete payload. Read %d/%d bytes. Error: %s\n", bytesRead, headerSize, err)
			return nil
		}
		sizeBytes := frameBuffer[:8]
		mask := frameBuffer[8:12]
		payloadLength64 := binary.BigEndian.Uint64(sizeBytes)
		data = wsServer.readFrameData(connection, mask, uint64(payloadLength64))
	}

	if wsServer.onMessage != nil {
		wsServer.onMessage(clientConnection, data)
	}

	return nil
}

func (wsServer *wsserver) readFrameData(connection net.Conn, mask []byte, length uint64) []byte {
	readBuffer := make([]byte, length)

	bytesRead, err := connection.Read(readBuffer)
	if bytesRead != int(length) || err != nil {
		debug.Println("Failed to read complete payload. Read %d/%d bytes. Error: %s\n", bytesRead, length, err)
		return nil
	}

	data := make([]byte, 0, length)
	for i := range readBuffer {
		data = append(data, readBuffer[i]^mask[i%4])
		if uint64(len(data)) == length {
			break
		}
	}

	return data
}

func (wsServer *wsserver) SendText(connection *ClientConnection, data []byte) error {
	controlByte := FINAL_FRAGMENT | OP_TEXT_FRAME
	return wsServer.send(connection, controlByte, data)
}

func (wsServer *wsserver) SendBinary(connection *ClientConnection, data []byte) error {
	controlByte := FINAL_FRAGMENT | OP_BINARY_FRAME
	return wsServer.send(connection, controlByte, data)
}

func (wsServer *wsserver) send(client *ClientConnection, controlByte byte, data []byte) error {
	if client.connection == nil {
		return &WSServerError{message: "Client connection is nil"}
	}

	payloadLength := len(data)
	frameLength := payloadLength + 2 // +2 for control byte and payload byte

	if payloadLength > 125 {
		frameLength += 2 // extra 2 payload length bytes
	}

	if payloadLength > (1 << 16) {
		frameLength += 4 // another extra 4 payload length bytes
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

	var bytesWritten int
	var writeError error

	client.mu.Lock()
	if client.connection != nil {
		// need to check connection for nil again because we could have spent time waiting here for a lock
		bytesWritten, writeError = client.connection.Write(payload)
	}
	client.mu.Unlock()

	if bytesWritten != len(payload) || writeError != nil {
		errorMsg := fmt.Sprintf("Failed to write to connection. Bytes written = %d/%d, Error = %s", bytesWritten, len(payload), writeError)
		return &WSServerError{message: errorMsg}
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
	clients := wsServer.snapshotClients()

	var wg sync.WaitGroup
	workers := runtime.NumCPU()
	jobs := make(chan *ClientConnection, len(clients))

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for client := range jobs {
				if err := wsServer.send(client, controlByte, data); err != nil {
					debug.Printf("Failed to broadcast to client %s: %s\nMessage attempted: %s / %v\n", client.id, err.Error(), string(data), data)
				}
			}
		}()
	}

	for _, client := range clients {
		jobs <- client
	}

	close(jobs)
	wg.Wait()
}

func (wsServer *wsserver) Shutdown(ctx context.Context) error {
	if !wsServer.active.Load() {
		return nil
	}

	wsServer.active.Store(false)
	shutdownErr := make(chan error, len(wsServer.clients))

	wsServer.clientsMutex.Lock()
	defer wsServer.clientsMutex.Unlock()

	for _, client := range wsServer.clients {
		shutdownErr <- wsServer.closeClient(client)
	}

	wsServer.server.Close()

	return nil
}

func (wsServer *wsserver) closeClient(clientConnection *ClientConnection) error {
	// force normal read goroutine to exit
	debug.Println("closing client")
	clientConnection.connection.SetReadDeadline(time.Now())
	<-clientConnection.closeSignal

	controlByte := FINAL_FRAGMENT | OP_CLOSE_CONN

	closeMessage := "Server closed connection"

	payload := make([]byte, len(closeMessage)+2)
	binary.BigEndian.PutUint16(payload, uint16(CLOSE_STATUS_NORMAL))
	copy(payload[2:], []byte(closeMessage))

	wsServer.send(clientConnection, controlByte, payload)

	// wait for client close confirmation
	closeBuf := make([]byte, 2)
	clientConnection.connection.SetDeadline(time.Now().Add(5 * time.Second))
	n, err := clientConnection.connection.Read(closeBuf)
	if n != len(closeBuf) || err != nil {
		return &WSServerError{message: "Error closing client"}
	}

	if closeBuf[0] != FINAL_FRAGMENT|OP_CLOSE_CONN {
		debug.Printf("Error: expected close response, got: %x\nClosing connection anyway...\n", closeBuf)
	}

	// TODO: Potentially want to read rest of frame to log status code or any close message

	if err = clientConnection.connection.Close(); err != nil {
		return err
	}

	return nil
}

func (wsServer *wsserver) Ping() {
	clients := wsServer.snapshotClients()
	for _, client := range clients {
		wsServer.send(client, FINAL_FRAGMENT|OP_PING, nil)
	}
}

func (wsServer *wsserver) pong(client *ClientConnection) {
	// TODO: Read payload size in case application data passed with ping, and read mask key
	// Application data must be sent back to the client
	wsServer.send(client, FINAL_FRAGMENT|OP_PONG, nil)
}

func (wsServer *wsserver) snapshotClients() []*ClientConnection {
	clientSnapshot := make([]*ClientConnection, 0)
	wsServer.clientsMutex.RLock()
	for _, connection := range wsServer.clients {
		clientSnapshot = append(clientSnapshot, connection)
	}
	wsServer.clientsMutex.RUnlock()
	return clientSnapshot
}

func (wsServer *wsserver) Clients() []*ClientConnection {
	clients := wsServer.snapshotClients()
	return clients
}

func generateClientID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
