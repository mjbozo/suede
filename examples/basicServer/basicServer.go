package main

import (
	"context"
	"fmt"

	"github.com/mjbozo/suede"
)

func main() {
	wsServer, wsErr := suede.WebSocketServer(8080, "/chat")
	if wsErr != nil {
		panic("Could not create WebSocket server")
	}

	wsServer.OnConnect(func(client *suede.ClientConnection) {
		fmt.Println("Client connected")
	})

	wsServer.OnDisconnect(func(client *suede.ClientConnection) {
		fmt.Println("Client disconnected")
	})

	wsServer.OnMessage(func(client *suede.ClientConnection, data []byte) {
		fmt.Printf("Message = %s\n", data)
		wsServer.BroadcastText([]byte("broadcasting..."))
	})

	done := make(chan error, 1)
	go func() {
		done <- wsServer.Start(context.Background())
	}()

	// add any additional logic here, which will be executed as normal program
	// for example:
	fmt.Println("Suede WebSocket server running")
	fmt.Printf("Port: %s\tPath: %s\n", fmt.Sprintf("%d", wsServer.Port), wsServer.Path)

	<-done
}
