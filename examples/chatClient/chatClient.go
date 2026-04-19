package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mjbozo/suede"
)

func main() {
	client, wsErr := suede.WebSocket("http://localhost:8080/chat")
	if wsErr != nil {
		panic("could not connect")
	}

	fmt.Print("Enter your name > ")
	reader := bufio.NewReader(os.Stdin)
	name, _ := reader.ReadString('\n')
	name = strings.ReplaceAll(name, "\n", "")

	client.OnConnect(func() {
		fmt.Println("Connected. Welcome to the chatroom.")
		client.SendText([]byte(name + " has joined the chat"))
	})

	client.OnMessage(func(data []byte) {
		fmt.Printf("%s\n", data)
	})

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	clientError := make(chan error, 1)

	go func() {
		clientError <- client.Start(context.Background())
	}()

	messages := make(chan string)
	go func() {
		for {
			message, err := reader.ReadString('\n')
			if err != nil {
				fmt.Println("Read error")
				continue
			}

			fmt.Print("\u001b[1A\u001b[2K")
			message = strings.ReplaceAll(message, "\n", "")

			messages <- message
		}
	}()

	for {
		select {
		case <-quit:
			client.Close()
			return
		case <-clientError:
			return
		case msg := <-messages:
			if msg == ":quit" {
				client.Close()
				return
			}

			client.SendText([]byte("[" + name + "] " + msg))
		}
	}
}
