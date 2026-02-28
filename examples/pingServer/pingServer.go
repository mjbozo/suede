package main

import (
	"fmt"
	"time"

	"github.com/mjbozo/suede"
)

func main() {
	wsServer, wsErr := suede.WebSocketServer(8080, "/ping")
	if wsErr != nil {
		panic("ws server failed to start")
	}

	wsServer.RunCallback(func() {
		fmt.Println("WebSocket server started on port 8080 at path /ping")
		tick := time.Tick(1000 * time.Millisecond)
		for range tick {
			fmt.Println("Pinging...")
			wsServer.Ping()
		}
	})
}
