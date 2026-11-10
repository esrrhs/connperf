package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"github.com/esrrhs/gohome/network"
	"io"
	"math/rand"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testReport(received, frames uint64, elapsed time.Duration) []byte {
	b := make([]byte, 32)
	copy(b, "CPREPORT")
	binary.BigEndian.PutUint64(b[8:16], received)
	binary.BigEndian.PutUint64(b[16:24], uint64(elapsed))
	binary.BigEndian.PutUint64(b[24:32], frames)
	return b
}
func TestProbeReceiverAccounting(t *testing.T) {
	var r probeResult
	// Sender queued 100 frames, but receiver only counts ten after warmup.
	if err := applyProbeReport(&r, testReport(160, 100, 2*time.Second), 100, 16, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if r.Bytes != 160 || r.AvgBps != 80 || r.Elapsed != 2*time.Second {
		t.Fatalf("used sender accounting: %+v", r)
	}
	for _, b := range [][]byte{nil, []byte("wrong"), testReport(160, 99, time.Second), testReport(1601, 100, time.Second), testReport(161, 100, time.Second), testReport(0, 100, time.Second), testReport(160, 100, 0), testReport(160, 100, 11*time.Second)} {
		if err := applyProbeReport(&probeResult{}, b, 100, 16, 10*time.Second); err == nil {
			t.Fatalf("accepted invalid report %v", b)
		}
	}
}
func TestProbeMedianAndMajority(t *testing.T) {
	r := probeResult{Samples: []probeResult{{AvgBps: 100}, {AvgBps: 10000}, {AvgBps: 90}}}
	summarizeProbe(&r, 3)
	if r.Err != nil || r.AvgBps != 100 {
		t.Fatalf("not median: %+v", r)
	}
	r = probeResult{Samples: []probeResult{{AvgBps: 100}, {AvgBps: 200}, {Err: fmt.Errorf("missing report")}}}
	summarizeProbe(&r, 3)
	if r.Err != nil || r.AvgBps != 150 {
		t.Fatalf("even median: %+v", r)
	}
	r = probeResult{Samples: []probeResult{{AvgBps: 99999}, {Err: io.EOF}, {Err: io.EOF}}}
	summarizeProbe(&r, 3)
	if r.Err == nil || selectProbeWinner([]probeResult{r}) != -1 {
		t.Fatal("failed majority became winner")
	}
}
func TestProbeRandomizedInterleaving(t *testing.T) {
	seed := func() []int { return probeSchedule(5, 3, rand.New(rand.NewSource(123)).Shuffle) }
	a, b := seed(), seed()
	if !reflect.DeepEqual(a, b) {
		t.Fatal("non deterministic injected schedule")
	}
	if reflect.DeepEqual(a[:5], []int{0, 1, 2, 3, 4}) {
		t.Fatal("not shuffled")
	}
	for round := 0; round < 3; round++ {
		seen := map[int]bool{}
		for _, i := range a[round*5 : (round+1)*5] {
			seen[i] = true
		}
		if len(seen) != 5 {
			t.Fatal("round does not visit every protocol once")
		}
	}
}
func TestProbeRejectsCorruptOrOversizeFrames(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			done := make(chan error, 1)
			go func() { defer server.Close(); done <- receiveProbe(server, 16, 0) }()
			var ready [1]byte
			if _, err := io.ReadFull(client, ready[:]); err != nil {
				t.Fatal(err)
			}
			hdr := make([]byte, 4)
			binary.BigEndian.PutUint32(hdr, 17)
			if corrupt {
				binary.BigEndian.PutUint32(hdr, 16)
			}
			if _, err := writeFull(client, hdr); err != nil {
				t.Fatal(err)
			}
			if corrupt {
				_, _ = writeFull(client, make([]byte, 16))
			}
			if err := <-done; err == nil {
				t.Fatal("validation accepted bad frame")
			}
			if _, err := client.Read(ready[:]); err == nil {
				t.Fatal("invented report after invalid data")
			}
		})
	}
}
func TestProbeGuardCancellationJoins(t *testing.T) {
	for i := 0; i < 20; i++ {
		client, server := net.Pipe()
		ctx, cancel := context.WithCancel(context.Background())
		stop := guardConn(ctx, func() { _ = client.Close() }, time.Second)
		done := make(chan struct{})
		go func() { defer close(done); _, _ = client.Read(make([]byte, 1)) }()
		cancel()
		stop()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("read worker not joined")
		}
		_ = server.Close()
	}
}

// Scripted TCP peers exercise missing reports and cancellation, independent of
// how many bytes TCP accepts into its local send buffer.
func scriptedProbePeer(t *testing.T, script func(net.Conn)) (string, <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		script(c)
	}()
	return ln.Addr().String(), done
}
func readProbeUpload(c net.Conn, size int) {
	_, _ = c.Write([]byte{'R'})
	var hdr [4]byte
	var count uint64
	for {
		if _, err := io.ReadFull(c, hdr[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n == 0 {
			return
		}
		if _, err := io.CopyN(io.Discard, c, int64(n)); err != nil {
			return
		}
		count++
		if count%probeBatchFrames(size) == 0 {
			_, _ = writeFull(c, probeProgress(count))
		}
	}
}
func TestProbeMissingReportAndReportCancellation(t *testing.T) {
	for _, cancelReport := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelReport), func(t *testing.T) {
			addr, done := scriptedProbePeer(t, func(c net.Conn) {
				readProbeUpload(c, 64)
				if cancelReport {
					_, _ = c.Read(make([]byte, 1))
				}
			})
			c, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			stop := guardConn(ctx, func() { _ = c.Close() }, time.Second)
			defer stop()
			var r probeResult
			err = sendProbe(ctx, c, c, 64, 20*time.Millisecond, 0, &r)
			if err == nil || r.AvgBps != 0 {
				t.Fatalf("missing report became a successful sample: %+v %v", r, err)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("peer leaked")
			}
		})
	}
}
func TestProbeCancellationDuringBlockedWrite(t *testing.T) {
	release := make(chan struct{})
	addr, done := scriptedProbePeer(t, func(c net.Conn) { _, _ = c.Write([]byte{'R'}); <-release })
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	stop := guardConn(ctx, func() { _ = c.Close() }, time.Second)
	defer stop()
	start := time.Now()
	err = sendProbe(ctx, c, c, maxProbePayload, time.Second, 0, &probeResult{})
	close(release)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("blocked write not canceled: %v", err)
	}
	<-done
}
func TestProbeCancellationDuringDial(t *testing.T) {
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	for _, proto := range []string{"rudp", "quic"} {
		t.Run(proto, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			c, err := dialProbe(ctx, proto, udp.LocalAddr().String(), Config{})
			if c != nil {
				_ = c.Close()
			}
			if err == nil || time.Since(start) > 2*time.Second {
				t.Fatalf("dial not canceled: %v (%s)", err, time.Since(start))
			}
		})
	}
}
func TestAutoWireRejectsLegacyAndBounds(t *testing.T) {
	if validateProbe(15, time.Second, 0) == nil || validateProbe(16, 61*time.Second, 0) == nil || validateProbe(maxProbePayload+1, time.Second, 0) == nil {
		t.Fatal("unbounded probe accepted")
	}
	if validateProbe(16, time.Second, 0) != nil {
		t.Fatal("valid configuration rejected")
	}
	if selectProbeWinner([]probeResult{{AvgBps: 0}}) != -1 {
		t.Fatal("zero-byte sample won")
	}
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- serveAutoConn(context.Background(), &pipeConn{Conn: server}, Config{}) }()
	_, _ = writeFull(client, make([]byte, 29))
	err := <-done
	_ = client.Close()
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("legacy version not rejected: %v", err)
	}
}

type pipeConn struct{ net.Conn }

func (*pipeConn) Name() string                        { return "pipe" }
func (*pipeConn) Info() string                        { return "pipe" }
func (*pipeConn) Dial(string) (network.Conn, error)   { return nil, fmt.Errorf("unsupported") }
func (*pipeConn) Listen(string) (network.Conn, error) { return nil, fmt.Errorf("unsupported") }
func (*pipeConn) Accept() (network.Conn, error)       { return nil, fmt.Errorf("unsupported") }

func TestAutoReceiverReportsEveryDefaultProtocol(t *testing.T) {
	base := reserveAutoBasePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", base)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runAutoServer(ctx, Config{Listen: addr, Read: true, BufSize: 1024, Unit: "MBps"}) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("auto server cleanup did not join")
		}
	}()
	waitServerOrFail(t, done, 500*time.Millisecond)
	for round := 0; round < 2; round++ {
		for _, spec := range defaultAutoProtos(false, false) {
			t.Run(fmt.Sprintf("%s/%d", spec.Proto, round), func(t *testing.T) {
				target, err := mapAutoAddr(addr, spec.Proto, false)
				if err != nil {
					t.Fatal(err)
				}
				probeCtx, stop := context.WithTimeout(ctx, 8*time.Second)
				defer stop()
				r := probeReceiverGoodput(probeCtx, spec.Proto, target, 1024, 80*time.Millisecond, 0, Config{})
				if r.Err != nil || r.Bytes == 0 || r.Elapsed <= 0 || r.AvgBps <= 0 {
					t.Fatalf("no valid receiver report: %+v", r)
				}
			})
		}
	}
}

func TestProbeControlCancellationCleansRegistryAndData(t *testing.T) {
	for _, claim := range []bool{false, true} {
		t.Run(fmt.Sprint(claim), func(t *testing.T) {
			hub := &probeHub{sessions: make(map[[16]byte]*probeSession)}
			client, server := net.Pipe()
			controlDone := make(chan error, 1)
			go func() {
				controlDone <- serveAutoConn(context.Background(), &pipeConn{Conn: server}, Config{Proto: "tcp", autoHub: hub})
			}()
			var token [16]byte
			token[0] = 1
			if _, err := writeFull(client, append(autoHeader('C', 16, time.Second, 0), token[:]...)); err != nil {
				t.Fatal(err)
			}
			var ack [1]byte
			if _, err := io.ReadFull(client, ack[:]); err != nil || ack[0] != 'H' {
				t.Fatalf("control setup: %v", err)
			}
			var dataDone chan error
			if claim {
				dataClient, dataServer := net.Pipe()
				defer dataClient.Close()
				dataDone = make(chan error, 1)
				go func() {
					dataDone <- serveAutoConn(context.Background(), &pipeConn{Conn: dataServer}, Config{Proto: "tcp", autoHub: hub})
				}()
				if _, err := writeFull(dataClient, append(autoHeader('P', 0, 0, 0), token[:]...)); err != nil {
					t.Fatal(err)
				}
				if _, err := io.ReadFull(client, ack[:]); err != nil || ack[0] != 'R' {
					t.Fatalf("data setup: %v", err)
				}
			}
			_ = client.Close()
			select {
			case <-controlDone:
			case <-time.After(time.Second):
				t.Fatal("control handler leaked after peer disconnected")
			}
			if claim {
				select {
				case <-dataDone:
				case <-time.After(time.Second):
					t.Fatal("data handler leaked after control canceled")
				}
			}
			hub.mu.Lock()
			n := len(hub.sessions)
			hub.mu.Unlock()
			if n != 0 {
				t.Fatal("session retained after cleanup")
			}
			if _, err := hub.claim(token); err == nil {
				t.Fatal("expired token reused")
			}
		})
	}
}

func TestProbeWarmupAndDrainUseReceiverClock(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan error, 1)
	go func() { defer server.Close(); done <- receiveProbe(server, 16, 30*time.Millisecond) }()
	var ack [1]byte
	if _, err := io.ReadFull(client, ack[:]); err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 20)
	binary.BigEndian.PutUint32(frame[:4], 16)
	payload := makePayload(16)
	for seq := uint64(0); seq < 3; seq++ {
		if seq == 1 {
			time.Sleep(60 * time.Millisecond)
		}
		binary.BigEndian.PutUint64(payload[:8], seq)
		copy(frame[4:], payload)
		if _, err := writeFull(client, frame); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = writeFull(client, make([]byte, 4))
	report := make([]byte, 32)
	if _, err := io.ReadFull(client, report); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var r probeResult
	if err := applyProbeReport(&r, report, 3, 16, time.Second); err != nil {
		t.Fatal(err)
	}
	// Frame 0 and frame 1 began before cutoff (frame 1 arrived after cutoff).
	if r.Bytes != 16 || r.Elapsed < 30*time.Millisecond {
		t.Fatalf("warmup/drain clamped or counted partial warmup frame: %+v", r)
	}
}
