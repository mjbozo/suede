package main

import (
	"context"
	"fmt"

	"github.com/mjbozo/suede"
)

func main() {
	wsClient, wsErr := suede.WebSocket("http://localhost:8080/chat")
	if wsErr != nil {
		fmt.Println("Unable to connect WebSocket")
		return
	}

	wsClient.OnConnect(func() {
		fmt.Println("Suede WebSocket client connected")
		wsClient.SendText([]byte("Hello I am a new client"))
	})

	wsClient.OnDisconnect(func() {
		fmt.Println("Disconnect callback")
	})

	wsClient.OnMessage(func(data []byte, isBinary bool) {
		fmt.Printf("Data = %s\n", data)
	})

	wsClient.Start(context.Background())
}
