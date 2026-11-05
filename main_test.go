package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
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

func TestPercentileDuration(t *testing.T) {
	if got := percentileDuration(nil, 50); got != 0 {
		t.Fatalf("empty: got %v", got)
	}
	if got := percentileDuration([]int64{42}, 50); got != 42 {
		t.Fatalf("single: got %v", got)
	}
	// 0,10,...,100 → n=11; p50 at rank 5 → 50; p90 at rank 9 → 90; p99 near 100
	sorted := make([]int64, 0, 11)
	for i := 0; i <= 10; i++ {
		sorted = append(sorted, int64(i*10))
	}
	if got := percentileDuration(sorted, 50); got != 50 {
		t.Fatalf("p50: got %v want 50", got)
	}
	if got := percentileDuration(sorted, 90); got != 90 {
		t.Fatalf("p90: got %v want 90", got)
	}
	if got := percentileDuration(sorted, 0); got != 0 {
		t.Fatalf("p0: got %v want 0", got)
	}
	if got := percentileDuration(sorted, 100); got != 100 {
		t.Fatalf("p100: got %v want 100", got)
	}
}

func TestRTTStatsMinAvgMaxPercentiles(t *testing.T) {
	var s rttStats
	for i := 1; i <= 100; i++ {
		s.add(int64(i) * int64(time.Millisecond))
	}
	count, min, avg, max, p50, p90, p99, ok := s.snapshotAndReset()
	if !ok || count != 100 {
		t.Fatalf("ok=%v count=%d", ok, count)
	}
	if min != time.Millisecond {
		t.Fatalf("min=%v want 1ms", min)
	}
	if max != 100*time.Millisecond {
		t.Fatalf("max=%v want 100ms", max)
	}
	// sum 1..100 = 5050 → avg 50.5ms
	if avg < 50*time.Millisecond || avg > 51*time.Millisecond {
		t.Fatalf("avg=%v want ~50.5ms", avg)
	}
	// linear interp on 1..100ms: p50≈50.5ms, p90≈90.1ms, p99≈99.01ms
	if p50 < 50*time.Millisecond || p50 > 51*time.Millisecond {
		t.Fatalf("p50=%v want ~50.5ms", p50)
	}
	if p90 < 90*time.Millisecond || p90 > 91*time.Millisecond {
		t.Fatalf("p90=%v want ~90.1ms", p90)
	}
	if p99 < 98*time.Millisecond || p99 > 100*time.Millisecond {
		t.Fatalf("p99=%v want ~99ms", p99)
	}
	// snapshot clears
	if _, _, _, _, _, _, _, ok2 := s.snapshotAndReset(); ok2 {
		t.Fatal("expected empty after snapshot")
	}
}

func TestRTTStatsRejectNegative(t *testing.T) {
	var s rttStats
	s.add(-1)
	if _, _, _, _, _, _, _, ok := s.snapshotAndReset(); ok {
		t.Fatal("negative sample should be ignored")
	}
}

func TestRTTStatsReservoirCap(t *testing.T) {
	var s rttStats
	n := rttSampleCap*3 + 7
	for i := 0; i < n; i++ {
		s.add(int64(i + 1))
	}
	s.mu.Lock()
	got := len(s.samples)
	count := s.count
	s.mu.Unlock()
	if got > rttSampleCap {
		t.Fatalf("reservoir len=%d > cap %d", got, rttSampleCap)
	}
	if got != rttSampleCap {
		t.Fatalf("reservoir len=%d want %d", got, rttSampleCap)
	}
	if count != int64(n) {
		t.Fatalf("count=%d want %d", count, n)
	}
	_, min, _, max, p50, p90, p99, ok := s.snapshotAndReset()
	if !ok {
		t.Fatal("expected samples")
	}
	if min != 1 {
		t.Fatalf("min=%v want 1", min)
	}
	if max != time.Duration(n) {
		t.Fatalf("max=%v want %d", max, n)
	}
	if !(min <= p50 && p50 <= p90 && p90 <= p99 && p99 <= max) {
		t.Fatalf("percentile order min=%v p50=%v p90=%v p99=%v max=%v", min, p50, p90, p99, max)
	}
}

func TestRTTStatsResetClearsSamples(t *testing.T) {
	var s rttStats
	s.add(int64(time.Millisecond))
	s.reset()
	if _, _, _, _, _, _, _, ok := s.snapshotAndReset(); ok {
		t.Fatal("reset should clear samples")
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
		Listen:     listenAddr,
		Proto:      proto,
		Read:       true,
		BufSize:    1024,
		Unit:       "MBps",
		RdnsDomain: "tunnel.rdns.test",
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
		Server:     dialAddr,
		Proto:      proto,
		Write:      true,
		BufSize:    1024,
		Duration:   1 * time.Second,
		Unit:       "MBps",
		RdnsDomain: "tunnel.rdns.test",
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

func TestEndToEndRDNS(t *testing.T) {
	port := getFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	runEndToEndProto(t, "rdns", addr, addr)
}

func TestEndToEndRDNS_PSK_Null(t *testing.T) {
	port := getFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	serverCfg := Config{
		Listen:        addr,
		Proto:         "rdns",
		Read:          true,
		BufSize:       1024,
		Unit:          "MBps",
		RdnsDomain:    "null.rdns.test",
		RdnsPSK:       "testpsk123",
		RdnsQueryType: "NULL",
	}

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- run(serverCtx, serverCfg)
	}()

	waitServerOrFail(t, serverDone, 200*time.Millisecond)

	clientCtx, clientCancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer clientCancel()

	clientCfg := Config{
		Server:        addr,
		Proto:         "rdns",
		Write:         true,
		BufSize:       1024,
		Duration:      1 * time.Second,
		Unit:          "MBps",
		RdnsDomain:    "null.rdns.test",
		RdnsPSK:       "testpsk123",
		RdnsQueryType: "NULL",
	}

	if err := run(clientCtx, clientCfg); err != nil {
		t.Fatalf("client rdns PSK run failed: %v", err)
	}

	serverCancel()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server rdns PSK run failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("server rdns PSK did not shut down in time")
	}
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

func TestEndToEndMultiConnTCP(t *testing.T) {
	port := getFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	serverCfg := Config{
		Listen:  addr,
		Proto:   "tcp",
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
		BufSize:  1024,
		Duration: 1 * time.Second,
		Unit:     "MBps",
		Conns:    2,
	}

	if err := run(clientCtx, clientCfg); err != nil {
		t.Fatalf("client multi-conn run failed: %v", err)
	}

	serverCancel()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server multi-conn run failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server multi-conn did not shut down in time")
	}
}

func TestWarmupTCP(t *testing.T) {
	port := getFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	serverCfg := Config{
		Listen:  addr,
		Proto:   "tcp",
		Read:    true,
		BufSize: 1024,
		Unit:    "MBps",
		Warmup:  time.Second,
	}

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- run(serverCtx, serverCfg)
	}()

	waitServerOrFail(t, serverDone, 200*time.Millisecond)

	clientCtx, clientCancel := context.WithTimeout(context.Background(), 3500*time.Millisecond)
	defer clientCancel()

	clientCfg := Config{
		Server:   addr,
		Proto:    "tcp",
		Write:    true,
		BufSize:  1024,
		Duration: 2 * time.Second,
		Unit:     "MBps",
		Warmup:   time.Second,
		Conns:    1,
	}

	if err := run(clientCtx, clientCfg); err != nil {
		t.Fatalf("client warmup run failed: %v", err)
	}

	serverCancel()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server warmup run failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server warmup did not shut down in time")
	}
}

func TestMaxConnTCP(t *testing.T) {
	port := getFreePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()

	serverCfg := Config{
		Listen:  addr,
		Proto:   "tcp",
		Read:    true,
		BufSize: 1024,
		Unit:    "MBps",
		MaxConn: 1,
	}

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- run(serverCtx, serverCfg)
	}()

	waitServerOrFail(t, serverDone, 200*time.Millisecond)

	// First client should succeed for a short run.
	client1Ctx, client1Cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer client1Cancel()

	client1Done := make(chan error, 1)
	go func() {
		client1Done <- run(client1Ctx, Config{
			Server:   addr,
			Proto:    "tcp",
			Write:    true,
			BufSize:  1024,
			Duration: 1500 * time.Millisecond,
			Unit:     "MBps",
			Conns:    1,
		})
	}()

	// Give first client time to occupy the sole slot.
	time.Sleep(200 * time.Millisecond)

	// Second client: dials succeed (TCP accept happens), but server closes
	// immediately when at maxconn. A short write-only run should complete
	// without hanging (connection may close quickly).
	client2Ctx, client2Cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer client2Cancel()

	_ = run(client2Ctx, Config{
		Server:   addr,
		Proto:    "tcp",
		Write:    true,
		BufSize:  1024,
		Duration: 500 * time.Millisecond,
		Unit:     "MBps",
		Conns:    1,
	})

	select {
	case err := <-client1Done:
		if err != nil {
			t.Fatalf("first client failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first client did not finish")
	}

	serverCancel()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server maxconn run failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server maxconn did not shut down in time")
	}
}

func TestIsBenignClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !isBenignClose(context.Canceled, ctx) {
		t.Fatal("canceled context should be benign")
	}
	if !isBenignClose(io.EOF, context.Background()) {
		t.Fatal("EOF should be benign")
	}
	if !isBenignClose(fmt.Errorf("write closed conn"), context.Background()) {
		t.Fatal("gohome closed error should be benign")
	}
	if isBenignClose(fmt.Errorf("no route to host"), context.Background()) {
		t.Fatal("unexpected network error should not be benign")
	}
}
