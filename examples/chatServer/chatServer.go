package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/mjbozo/suede"
)

func main() {
	server, wsErr := suede.WebSocketServer(8080, "/chat")
	if wsErr != nil {
		panic("could not start server")
	}

	server.OnConnect(func(client net.Conn) {
		server.BroadcastText([]byte("New user joined the chat!"))
	})

	server.OnDisconnect(func(client net.Conn) {
		server.BroadcastText([]byte("User has left the chat"))
	})

	server.OnMessage(func(client net.Conn, data []byte) {
		server.BroadcastText(data)
	})

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	serverError := make(chan error, 1)

	go func() {
		fmt.Println("Server starting")
		serverError <- server.Start(context.Background())
	}()

	select {
	case <-quit:
	case <-serverError:
	}

	server.Shutdown(context.Background())
}
