# Suede - A Go WebSockets Package

> 100% pass on server Autobahn Testsuite

> 100% pass on client Autobahn Testsuite

Suede WebSockets is a Go WebSocket package which provides an extremely simple and easy to use
WebSocket interface.

Suede WebSockets is under initial development, but very basic functionality already exists.
See examples for current capabilities.

## Examples
### Client
```go
import (
    "context"
	"fmt"
	"github.com/mjbozo/suede"
)

func main() {
	// create Suede WebSocket
	wsClient, wsErr := suede.WebSocket("http://localhost:8080/chat")
	if wsErr != nil {
		panic("WS Client unable to connect")
	}

	// define behavior when client connects to server
	wsClient.OnConnect(func() {
		fmt.Println("Connected to server")
	})

	// define behaviour when client disconnects from server
	wsClient.OnDisconnect(func() {
		fmt.Println("Disconnected from server")
	})

	// define behaviour when client receives message from server
	wsClient.OnMessage(func(data []byte, isBinary bool) {
		fmt.Printf("Received message: %s\n", data)
	})

	// connect and run client
	wsClient.Start(context.Background())
}
```

### Server
 ```go
import (
    "context"
	"fmt"
	 "github.com/mjbozo/suede"
)

func main() {
	// create Suede WebSocket server
	wsServer, wsErr := suede.WebSocketServer(8080, "/chat")
	if wsErr != nil {
		panic("Could not create WebSocket server")
	}

	// define behaviour when client connects to server
	wsServer.OnConnect(func(client *ClientConnection) {
		fmt.Println("Client connected")
	})

	// define behaviour when client disconnects from server
	wsServer.OnDisconnect(func(client *ClientConnection) {
		fmt.Println("Client disconnected")
	})

	// define behaviour when server received message from client
	wsServer.OnMessage(func(client *ClientConnection, data []byte, isBinary bool) {
		fmt.Printf("Received message: %s\n", data)
	}

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
```

---

*Disclaimer: This package was created as a hobbyist learning project. It is not recommended for production use.*
