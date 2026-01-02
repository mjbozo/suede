# Suede - A Go WebSockets Package

Suede WebSockets is a Go WebSocket package which provides an extremely simple and easy to use
WebSocket interface.

Suede WebSockets is under initial development, but very basic functionality already exists.
See examples for current capabilities.

## Examples
### Client
```go
import (
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
	wsClient.OnConnect = func() {
		fmt.Println("Connected to server")
	}

	// define behaviour when client disconnects from server
	wsClient.OnDisconnect = func() {
		fmt.Println("Disconnected from server")
	}

	// define behaviour when client receives message from server
	wsClient.OnMessage = func(data []byte) {
		fmt.Printf("Received message: %s\n", data)
	}

	// connect and run client
	wsClient.Run()
}
```

If additional control is required, a callback can be used to run while the WebSocket client is connected:
```go
...

wsClient.RunCallback(func() {
	// this code runs after client connects
	fmt.Println("Client running...")
})
```

Even further control can be gained by calling `Connect()` directly and managing a `sync.WaitGroup`.
```go
import (
	...
	"sync"
)

...

var wg sync.WaitGroup
connectErr := wsClient.Connect(&wg)
if connectErr != nil {
	panic("WebSocket client failed to connect")
}

// this code runs as normal after client connects
fmt.Println("Client running...")

// manually wait for client to disconnect
wg.Wait()
```
### Server
 ```go
import (
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
	wsServer.OnConnect = func() {
		fmt.Println("Client connected")
	}

	// define behaviour when client disconnects from server
	wsServer.OnDisconnect = func() {
		fmt.Println("Client disconnected")
	}

	// define behaviour when server received message from client
	wsServer.OnMessage = func(data []byte) {
		fmt.Printf("Received message: %s\n", data)
	}

	// start the server
	wsServer.Run()
}
```

Like the client, additional control can be gained by calling either `RunCallback()` or `Start()` methods on the WebSocket server.
```go
...

wsServer.RunCallback(func() {
	// this code runs after server starts
	fmt.Println("Server running...")
})
```


```go
import (
	...
	"sync"
)

...

var wg sync.WaitGroup
wsServer.Start(&wg)

// this code runs as normal after server starts
fmt.Println("Server running...")

// manually wait for server to shutdown
wg.Wait()
```

---

*Disclaimer: This package was created as a hobbyist learning project. It is not recommended for production use.*
