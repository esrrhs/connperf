package main

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/esrrhs/connperf/version"
)

func getFreePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to get free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestVersionPackage(t *testing.T) {
	if version.Version == "" {
		t.Fatal("empty version")
	}
	if len(version.Full()) == 0 {
		t.Fatal("empty full version")
	}
	if len(version.Short()) == 0 {
		t.Fatal("empty short version")
	}
}

func TestConfigValidation(t *testing.T) {
	cfg := Config{
		Server:  "",
		Listen:  "",
		Proto:   "tcp",
		Write:   true,
		Read:    false,
		BufSize: 1024,
	}
	// Neither server nor listen
	err := run(context.Background(), cfg)
	if err != nil {
		t.Errorf("expected nil error for empty config, got: %v", err)
	}
}

func TestEndToEndTCP(t *testing.T) {
	port := getFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	serverCfg := Config{
		Listen:  addr,
		Proto:   "tcp",
		Read:    true,
		BufSize: 64 * 1024,
	}

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- run(serverCtx, serverCfg)
	}()

	// Wait briefly for server listener to start
	time.Sleep(100 * time.Millisecond)

	clientCtx, clientCancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer clientCancel()

	clientCfg := Config{
		Server:   addr,
		Proto:    "tcp",
		Write:    true,
		BufSize:  64 * 1024,
		Duration: 1 * time.Second,
	}

	err := run(clientCtx, clientCfg)
	if err != nil {
		t.Fatalf("client run failed: %v", err)
	}

	serverCancel()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server run failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down in time")
	}
}

func TestEndToEndBidirectionalTCP(t *testing.T) {
	port := getFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	serverCfg := Config{
		Listen:  addr,
		Proto:   "tcp",
		Write:   true,
		Read:    true,
		BufSize: 32 * 1024,
	}

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- run(serverCtx, serverCfg)
	}()

	time.Sleep(100 * time.Millisecond)

	clientCtx, clientCancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer clientCancel()

	clientCfg := Config{
		Server:   addr,
		Proto:    "tcp",
		Write:    true,
		Read:     true,
		BufSize:  32 * 1024,
		Duration: 1 * time.Second,
	}

	err := run(clientCtx, clientCfg)
	if err != nil {
		t.Fatalf("client bidirectional run failed: %v", err)
	}

	serverCancel()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server bidirectional run failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down in time")
	}
}

func TestEndToEndKCP(t *testing.T) {
	port := getFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	serverCfg := Config{
		Listen:  addr,
		Proto:   "kcp",
		Read:    true,
		BufSize: 32 * 1024,
	}

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- run(serverCtx, serverCfg)
	}()

	time.Sleep(100 * time.Millisecond)

	clientCtx, clientCancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer clientCancel()

	clientCfg := Config{
		Server:   addr,
		Proto:    "kcp",
		Write:    true,
		BufSize:  32 * 1024,
		Duration: 1 * time.Second,
	}

	err := run(clientCtx, clientCfg)
	if err != nil {
		t.Fatalf("client kcp run failed: %v", err)
	}

	serverCancel()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server kcp run failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down in time")
	}
}
