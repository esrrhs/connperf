package main

import (
	"bytes"
	"context"
	"encoding/binary"
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

func TestMakePayload(t *testing.T) {
	p := makePayload(1024)
	if len(p) != 1024 {
		t.Fatalf("payload size = %d, want 1024", len(p))
	}
	for i := 0; i < 1024; i++ {
		if p[i] != byte(i%256) {
			t.Fatalf("payload[%d] = %d, want %d", i, p[i], byte(i%256))
		}
	}
	if !bytes.Equal(p, makePayload(1024)) {
		t.Fatal("payload should be deterministic")
	}
}

func TestNormalizeUnit(t *testing.T) {
	cases := []struct {
		in   string
		want string
		fail bool
	}{
		{"", "MBps", false},
		{"MBps", "MBps", false},
		{"MB/s", "MBps", false},
		{"Mbps", "Mbps", false},
		{"mbps", "Mbps", false},
		{"Mbit/s", "Mbps", false},
		{"Gbps", "Gbps", false},
		{"gbps", "Gbps", false},
		{"Gbit/s", "Gbps", false},
		{"bogus", "", true},
	}
	for _, tc := range cases {
		got, err := normalizeUnit(tc.in)
		if tc.fail {
			if err == nil {
				t.Fatalf("normalizeUnit(%q) expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("normalizeUnit(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("normalizeUnit(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatThroughput(t *testing.T) {
	// 2 MiB in 1 second → 2.00 MB/s
	v, lbl := formatThroughput(2*1024*1024, 1, "MBps")
	if lbl != "MB/s" || v < 1.99 || v > 2.01 {
		t.Fatalf("MBps: got %f %s", v, lbl)
	}
	// 1_000_000 bytes in 1s → 8 Mbps
	v, lbl = formatThroughput(1_000_000, 1, "Mbps")
	if lbl != "Mbps" || v < 7.99 || v > 8.01 {
		t.Fatalf("Mbps: got %f %s", v, lbl)
	}
	// 125_000_000 bytes in 1s → 1 Gbps
	v, lbl = formatThroughput(125_000_000, 1, "Gbps")
	if lbl != "Gbps" || v < 0.99 || v > 1.01 {
		t.Fatalf("Gbps: got %f %s", v, lbl)
	}
}

func TestHeaderEndianness(t *testing.T) {
	buf := make([]byte, headerSize)
	seq := uint64(0x0102030405060708)
	ns := uint64(0x1122334455667788)
	binary.LittleEndian.PutUint64(buf[0:8], seq)
	binary.LittleEndian.PutUint64(buf[8:16], ns)
	if binary.LittleEndian.Uint64(buf[0:8]) != seq {
		t.Fatal("seq roundtrip failed")
	}
	if binary.LittleEndian.Uint64(buf[8:16]) != ns {
		t.Fatal("ns roundtrip failed")
	}
}

func TestJitterStateRFC3550ish(t *testing.T) {
	var j jitterState
	base := time.Now()
	j.onArrival(base)
	j.onArrival(base.Add(10 * time.Millisecond))
	j.onArrival(base.Add(20 * time.Millisecond)) // stable 10ms gaps → low jitter after first D
	j.onArrival(base.Add(40 * time.Millisecond)) // 20ms gap → introduces jitter
	d, ok := j.value()
	if !ok {
		t.Fatal("expected jitter samples")
	}
	if d < 0 {
		t.Fatalf("jitter negative: %v", d)
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
		Unit:    "MBps",
	}
	// Neither server nor listen
	err := run(context.Background(), cfg)
	if err != nil {
		t.Errorf("expected nil error for empty config, got: %v", err)
	}
}

func waitServerOrFail(t *testing.T, serverDone <-chan error, wait time.Duration) {
	t.Helper()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server exited before ready: %v", err)
		}
		t.Fatal("server exited before ready")
	case <-timer.C:
	}
}

// runEndToEndProto starts a read-only server and a write-only client for ~1s.
func runEndToEndProto(t *testing.T, proto, listenAddr, dialAddr string) {
	t.Helper()

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	serverCfg := Config{
		Listen:  listenAddr,
		Proto:   proto,
		Read:    true,
		BufSize: 1024,
		Unit:    "MBps",
	}

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- run(serverCtx, serverCfg)
	}()

	// Catch bind/listen failures quickly; give the listener a short settle time.
	waitServerOrFail(t, serverDone, 200*time.Millisecond)

	// Client timeout leaves headroom for handshake (quic/rhttp) + 1s duration.
	clientCtx, clientCancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer clientCancel()

	clientCfg := Config{
		Server:   dialAddr,
		Proto:    proto,
		Write:    true,
		BufSize:  1024,
		Duration: 1 * time.Second,
		Unit:     "MBps",
	}

	if err := run(clientCtx, clientCfg); err != nil {
		t.Fatalf("client %s run failed: %v", proto, err)
	}

	serverCancel()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server %s run failed: %v", proto, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("server %s did not shut down in time", proto)
	}
}

func TestEndToEndTCP(t *testing.T) {
	port := getFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	runEndToEndProto(t, "tcp", addr, addr)
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
		BufSize: 1024,
		Unit:    "MBps",
	}

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- run(serverCtx, serverCfg)
	}()

	waitServerOrFail(t, serverDone, 200*time.Millisecond)

	clientCtx, clientCancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer clientCancel()

	clientCfg := Config{
		Server:   addr,
		Proto:    "tcp",
		Write:    true,
		Read:     true,
		BufSize:  1024,
		Duration: 1 * time.Second,
		Unit:     "MBps",
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
	case <-time.After(3 * time.Second):
		t.Fatal("server did not shut down in time")
	}
}

func TestEndToEndKCP(t *testing.T) {
	port := getFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	runEndToEndProto(t, "kcp", addr, addr)
}

func TestEndToEndRUDP(t *testing.T) {
	port := getFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	runEndToEndProto(t, "rudp", addr, addr)
}

func TestEndToEndQUIC(t *testing.T) {
	port := getFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	runEndToEndProto(t, "quic", addr, addr)
}

func TestEndToEndRHTTP(t *testing.T) {
	port := getFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	runEndToEndProto(t, "rhttp", addr, addr)
}

// TestEndToEndRICMP exercises ICMP-based transport. Raw ip4:icmp sockets need
// root or CAP_NET_RAW; without that privilege we skip so CI stays green.
func TestEndToEndRICMP(t *testing.T) {
	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	serverCfg := Config{
		Listen:  "127.0.0.1",
		Proto:   "ricmp",
		Read:    true,
		BufSize: 1024,
		Unit:    "MBps",
	}

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- run(serverCtx, serverCfg)
	}()

	select {
	case err := <-serverDone:
		if err != nil {
			t.Skipf("ricmp requires root/CAP_NET_RAW for ip4:icmp sockets: %v", err)
		}
		t.Fatal("ricmp server exited before ready")
	case <-time.After(200 * time.Millisecond):
		// Listen succeeded.
	}

	clientCtx, clientCancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer clientCancel()

	clientCfg := Config{
		Server:   "127.0.0.1",
		Proto:    "ricmp",
		Write:    true,
		BufSize:  1024,
		Duration: 1 * time.Second,
		Unit:     "MBps",
	}

	if err := run(clientCtx, clientCfg); err != nil {
		serverCancel()
		t.Fatalf("client ricmp run failed: %v", err)
	}

	serverCancel()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server ricmp run failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server ricmp did not shut down in time")
	}
}
