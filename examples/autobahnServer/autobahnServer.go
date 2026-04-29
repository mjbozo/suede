package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mjbozo/suede"
)

func main() {
	server, wsErr := suede.WebSocketServer(9001, "/")
	if wsErr != nil {
		panic("could not start server")
	}

	server.OnMessage(func(client *suede.ClientConnection, data []byte, isBinary bool) {
		if isBinary {
			server.SendBinary(client, data)
		} else {
			server.SendText(client, data)
		}
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
