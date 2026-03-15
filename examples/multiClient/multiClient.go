package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/mjbozo/suede"
)

func main() {
	var wg sync.WaitGroup

	numClients := 10000
	fmt.Printf("Spinning up %d clients to connect to localhost:8080/chat\n", numClients)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	ctx, cancelFunc := context.WithCancel(context.Background())

	for i := range numClients {
		wg.Add(1)
		client, err := suede.WebSocket("http://localhost:8080/chat")
		if err != nil {
			panic(err)
		}

		client.OnConnect(func() {
			client.SendText(fmt.Appendf(nil, "Hello from client #%d", i))
		})

		client.OnMessage(func(b []byte) {
			fmt.Println(string(b))
		})

		client.OnDisconnect(func() {
			wg.Done()
		})

		go func() {
			err := client.Start(ctx)
			if err != nil {
				fmt.Printf("Error: %s\n", err.Error())
			}
		}()
	}

	done := make(chan any, 1)

	go func() {
		wg.Wait()
		done <- nil
	}()

	select {
	case <-quit:
		cancelFunc()
	case <-done:
	}
}
