package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mjbozo/suede"
)

func main() {
	wsServer, wsErr := suede.WebSocketServer(8080, "/ping")
	if wsErr != nil {
		panic("ws server failed to start")
	}

	go wsServer.Start(context.Background())

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	fmt.Printf("WebSocket server started on port %d at path %s\n", wsServer.Port, wsServer.Path)
	for {
		select {
		case <-time.Tick(1000 * time.Millisecond):
			fmt.Println("Pinging...")
			wsServer.Ping()
		case <-quit:
			wsServer.Shutdown(context.Background())
			return
		}
	}
}
