package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// reserveAutoBasePort finds a base port where base..base+4 are all free on 127.0.0.1.
func reserveAutoBasePort(t *testing.T) int {
	t.Helper()
	for attempt := 0; attempt < 50; attempt++ {
		base := getFreePort(t)
		ok := true
		var listeners []net.Listener
		for off := 0; off <= 4; off++ {
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", base+off))
			if err != nil {
				ok = false
				break
			}
			listeners = append(listeners, ln)
		}
		for _, ln := range listeners {
			_ = ln.Close()
		}
		if ok {
			// Brief pause so the OS releases the ports before auto server binds.
			time.Sleep(50 * time.Millisecond)
			return base
		}
	}
	t.Fatal("could not reserve contiguous auto base ports")
	return 0
}

func TestMapAutoAddrPortScheme(t *testing.T) {
	cases := []struct {
		base     string
		proto    string
		hostOnly bool
		want     string
	}{
		{":8888", "tcp", false, ":8888"},
		{":8888", "rudp", false, ":8889"},
		{":8888", "kcp", false, ":8890"},
		{":8888", "quic", false, ":8891"},
		{":8888", "rhttp", false, ":8892"},
		{"127.0.0.1:9000", "tcp", false, "127.0.0.1:9000"},
		{"127.0.0.1:9000", "rudp", false, "127.0.0.1:9001"},
		{"127.0.0.1:9000", "kcp", false, "127.0.0.1:9002"},
		{"127.0.0.1:9000", "quic", false, "127.0.0.1:9003"},
		{"127.0.0.1:9000", "rhttp", false, "127.0.0.1:9004"},
		{"127.0.0.1:9000", "ricmp", true, "127.0.0.1"},
		{":9000", "ricmp", true, "0.0.0.0"},
		{"192.168.1.1", "ricmp", true, "192.168.1.1"},
	}
	for _, tc := range cases {
		got, err := mapAutoAddr(tc.base, tc.proto, tc.hostOnly)
		if err != nil {
			t.Fatalf("mapAutoAddr(%q,%q,%v): %v", tc.base, tc.proto, tc.hostOnly, err)
		}
		if got != tc.want {
			t.Fatalf("mapAutoAddr(%q,%q,%v)=%q want %q", tc.base, tc.proto, tc.hostOnly, got, tc.want)
		}
	}
}

func TestMapAutoAddrErrors(t *testing.T) {
	if _, err := mapAutoAddr("", "tcp", false); err == nil {
		t.Fatal("expected error for empty addr")
	}
	if _, err := mapAutoAddr("not-an-addr", "tcp", false); err == nil {
		t.Fatal("expected error for missing port")
	}
	if _, err := mapAutoAddr(":65535", "rhttp", false); err == nil {
		t.Fatal("expected overflow error for base+4")
	}
	if _, err := mapAutoAddr(":1000", "udp", false); err == nil {
		t.Fatal("expected error for unmapped proto")
	}
}

func TestAutoPortOffset(t *testing.T) {
	want := map[string]int{"tcp": 0, "rudp": 1, "kcp": 2, "quic": 3, "rhttp": 4}
	for proto, off := range want {
		got, ok := autoPortOffset(proto)
		if !ok || got != off {
			t.Fatalf("autoPortOffset(%q)=%d,%v want %d,true", proto, got, ok, off)
		}
	}
	if _, ok := autoPortOffset("ricmp"); ok {
		t.Fatal("ricmp should not have a numeric port offset")
	}
}

func TestDefaultAutoProtosExcludesRICMP(t *testing.T) {
	list := defaultAutoProtos(false)
	for _, s := range list {
		if s.Proto == "ricmp" {
			t.Fatal("ricmp should be excluded by default")
		}
	}
	if len(list) != 5 {
		t.Fatalf("len=%d want 5", len(list))
	}
	with := defaultAutoProtos(true)
	if len(with) != 6 || with[5].Proto != "ricmp" || !with[5].HostOnly || !with[5].Optional {
		t.Fatalf("unexpected with-ricmp list: %+v", with)
	}
}

func TestSelectProbeWinner(t *testing.T) {
	results := []probeResult{
		{Proto: "tcp", AvgBps: 100, Err: nil},
		{Proto: "rudp", AvgBps: 250, Err: nil},
		{Proto: "kcp", AvgBps: 200, Err: nil},
		{Proto: "quic", AvgBps: 0, Err: fmt.Errorf("dial fail")},
	}
	if got := selectProbeWinner(results); got != 1 {
		t.Fatalf("winner=%d want 1 (rudp)", got)
	}
	// Tie: earlier wins
	tie := []probeResult{
		{Proto: "tcp", AvgBps: 50},
		{Proto: "rudp", AvgBps: 50},
	}
	if got := selectProbeWinner(tie); got != 0 {
		t.Fatalf("tie winner=%d want 0", got)
	}
	if got := selectProbeWinner(nil); got != -1 {
		t.Fatalf("empty winner=%d want -1", got)
	}
	allFail := []probeResult{{Proto: "tcp", Err: fmt.Errorf("x")}}
	if got := selectProbeWinner(allFail); got != -1 {
		t.Fatalf("all-fail winner=%d want -1", got)
	}
}

func TestFormatProbeTableMarksWinner(t *testing.T) {
	results := []probeResult{
		{Proto: "tcp", Addr: "127.0.0.1:1", Bytes: 1024 * 1024, Elapsed: time.Second, AvgBps: 1024 * 1024},
		{Proto: "rudp", Addr: "127.0.0.1:2", Err: fmt.Errorf("boom")},
	}
	out := formatProbeTable(results, "MBps", 0)
	if !strings.Contains(out, "winner") {
		t.Fatalf("missing winner mark:\n%s", out)
	}
	if !strings.Contains(out, "FAIL") {
		t.Fatalf("missing FAIL row:\n%s", out)
	}
	if !strings.Contains(out, "tcp") || !strings.Contains(out, "rudp") {
		t.Fatalf("missing proto names:\n%s", out)
	}
}

func TestIsAutoProto(t *testing.T) {
	if !isAutoProto("auto") || !isAutoProto("AUTO") || !isAutoProto(" Auto ") {
		t.Fatal("expected auto match")
	}
	if isAutoProto("tcp") || isAutoProto("") {
		t.Fatal("unexpected auto match")
	}
}

// TestEndToEndAutoProbeOnly starts an auto server on localhost and runs a short
// client probe-only pass. Uses a free base port and short -probe-t.
func TestEndToEndAutoProbeOnly(t *testing.T) {
	basePort := reserveAutoBasePort(t)
	listen := fmt.Sprintf("127.0.0.1:%d", basePort)
	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- run(serverCtx, Config{
			Listen:  listen,
			Proto:   "auto",
			Read:    true,
			BufSize: 1024,
			Unit:    "MBps",
		})
	}()

	waitServerOrFail(t, serverDone, 500*time.Millisecond)

	clientCtx, clientCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer clientCancel()

	err := run(clientCtx, Config{
		Server:    listen,
		Proto:     "auto",
		Write:     true,
		BufSize:   1024,
		Unit:      "MBps",
		ProbeTime: time.Second,
		ProbeOnly: true,
		Warmup:    0,
	})
	if err != nil {
		serverCancel()
		t.Fatalf("auto probe-only failed: %v", err)
	}

	serverCancel()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("auto server failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("auto server did not shut down in time")
	}
}

func TestEndToEndAutoFullShort(t *testing.T) {
	basePort := reserveAutoBasePort(t)
	listen := fmt.Sprintf("127.0.0.1:%d", basePort)

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- run(serverCtx, Config{
			Listen:  listen,
			Proto:   "auto",
			Read:    true,
			BufSize: 1024,
			Unit:    "MBps",
		})
	}()

	waitServerOrFail(t, serverDone, 500*time.Millisecond)

	clientCtx, clientCancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer clientCancel()

	err := run(clientCtx, Config{
		Server:    listen,
		Proto:     "auto",
		Write:     true,
		BufSize:   1024,
		Unit:      "MBps",
		ProbeTime: time.Second,
		Duration:  time.Second,
		Conns:     1,
	})
	if err != nil {
		serverCancel()
		t.Fatalf("auto full run failed: %v", err)
	}

	serverCancel()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("auto server failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("auto server did not shut down in time")
	}
}
