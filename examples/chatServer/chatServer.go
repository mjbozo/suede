package main

import (
	"fmt"
	"net"

	"github.com/mjbozo/suede"
)

func main() {
	server, wsErr := suede.WebSocketServer(8080, "/chat")
	if wsErr != nil {
		panic("could not start server")
	}

	server.OnConnect = func(client net.Conn) {
		server.BroadcastText([]byte("New user joined the chat!"))
	}

	server.OnDisconnect = func() {
		server.BroadcastText([]byte("User has left the chat"))
	}

	server.OnMessage = func(client net.Conn, data []byte) {
		server.BroadcastText(data)
	}

	fmt.Println("Server starting")
	server.Run()
}
