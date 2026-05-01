package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mjbozo/suede"
)

func main() {
	fmt.Println("Suede Client getting test case count...")
	count, err := getCaseCount()
	if err != nil {
		fmt.Printf("Failed to get case count: %v\n", err)
		return
	}

	fmt.Printf("Running %d cases\n", count)

	for i := 1; i <= count; i++ {
		fmt.Printf("Running case %d/%d\n", i, count)
		if err := runCase(i); err != nil {
			fmt.Printf("Case %d error: %v\n", i, err)
		}
	}

	fmt.Println("Updating reports...")
	if err := updateReports(); err != nil {
		fmt.Printf("Failed to update reports: %v\n", err)
		return
	}

	fmt.Println("Done!")
}

func getCaseCount() (int, error) {
	count := 0
	done := make(chan *struct{})

	client, err := suede.WebSocket("ws://localhost:9001/getCaseCount")
	if err != nil {
		return 0, err
	}

	client.OnMessage(func(data []byte, isBinary bool) {
		fmt.Printf("%t, %s\n", isBinary, data)
		n, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil {
			count = n
		}
		done <- nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var clientErr error
	go func() {
		clientErr = client.Start(ctx)
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}

	client.Close()
	cancel()

	if clientErr != nil {
		return 0, clientErr
	}

	return count, nil
}

func runCase(caseNumber int) error {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("Test %d panicked: %v", caseNumber, r)
		}
	}()

	url := fmt.Sprintf("ws://localhost:9001/runCase?case=%d&agent=suede", caseNumber)
	client, err := suede.WebSocket(url)
	if err != nil {
		return err
	}

	client.OnMessage(func(data []byte, isBinary bool) {
		if isBinary {
			client.SendBinary(data)
		} else {
			client.SendText(data)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	return client.Start(ctx)
}

func updateReports() error {
	client, err := suede.WebSocket("ws://localhost:9001/updateReports?agent=suede")
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	return client.Start(ctx)
}
