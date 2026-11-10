package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/esrrhs/gohome/network"
)

// Auto wire v2 is intentionally separate from the unframed single-protocol
// benchmark. Fixed-size controls and bounded data frames prevent unbounded
// allocation. A probe is upload-only, regardless of full-run direction flags.
const autoMagic = "CPAUTO02"
const probeWait = 3 * time.Second
const maxProbeWindow = 60 * time.Second
const maxProbePayload = 1 << 20

// Bound application-level outstanding probe data to 1 MiB on every transport.
// Progress ACKs provide backpressure even when a transport buffers writes.
func probeBatchFrames(size int) uint64 { return uint64(maxProbePayload / size) }
func probeProgress(count uint64) []byte {
	b := make([]byte, 9)
	b[0] = 'B'
	binary.BigEndian.PutUint64(b[1:], count)
	return b
}

// guardConn closes exactly once on cancellation/deadline. stop joins its
// watcher; callers never abandon blocked IO workers after a timeout.
func guardConn(ctx context.Context, closeConn func(), budget time.Duration) func() {
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		var timeout <-chan time.Time
		if budget > 0 {
			timer := time.NewTimer(budget)
			defer timer.Stop()
			timeout = timer.C
		}
		select {
		case <-ctx.Done():
			closeConn()
		case <-timeout:
			closeConn()
		case <-stop:
			if ctx.Err() != nil {
				closeConn()
			}
		}
	}()
	return func() { close(stop); <-done }
}

func autoHeader(mode byte, size int, dur, warm time.Duration) []byte {
	b := make([]byte, 29)
	copy(b, autoMagic)
	b[8] = mode
	binary.BigEndian.PutUint32(b[9:13], uint32(size))
	binary.BigEndian.PutUint64(b[13:21], uint64(dur))
	binary.BigEndian.PutUint64(b[21:29], uint64(warm))
	return b
}

func autoFullHandshake(ctx context.Context, c network.Conn) error {
	var once sync.Once
	stop := guardConn(ctx, func() { once.Do(func() { _ = c.Close() }) }, probeWait)
	defer stop()
	if _, err := writeFull(c, autoHeader('F', 0, 0, 0)); err != nil {
		return err
	}
	var ack [1]byte
	if _, err := io.ReadFull(c, ack[:]); err != nil {
		return fmt.Errorf("auto v2 full handshake: %w", err)
	}
	if ack[0] != 'R' {
		return fmt.Errorf("incompatible auto server")
	}
	return nil
}

func serveAutoConn(ctx context.Context, c network.Conn, cfg Config) error {
	var once sync.Once
	closeConn := func() { once.Do(func() { _ = c.Close() }) }
	defer closeConn()
	stop := guardConn(ctx, closeConn, probeWait)
	h := make([]byte, 29)
	_, err := io.ReadFull(c, h)
	stop()
	if err != nil {
		return err
	}
	if string(h[:8]) != autoMagic {
		return fmt.Errorf("auto wire version mismatch: both endpoints need v2")
	}
	if h[8] == 'F' {
		stop = guardConn(ctx, closeConn, probeWait)
		_, err = writeFull(c, []byte{'R'})
		stop()
		if err != nil {
			return err
		}
		// Only full tests use the server's ordinary read/write configuration.
		stop = guardConn(ctx, closeConn, 0)
		defer stop()
		return showMany(ctx, []network.Conn{c}, cfg)
	}
	if cfg.autoHub == nil {
		return fmt.Errorf("auto probe control unavailable")
	}
	if h[8] != 'P' && h[8] != 'C' {
		return fmt.Errorf("unknown auto mode")
	}
	var token [16]byte
	stop = guardConn(ctx, closeConn, probeWait)
	_, err = io.ReadFull(c, token[:])
	stop()
	if err != nil {
		return err
	}
	if h[8] == 'P' {
		return cfg.autoHub.serveData(c, token)
	}
	if cfg.Proto != "tcp" {
		return fmt.Errorf("probe control requires base TCP listener")
	}
	size := int(binary.BigEndian.Uint32(h[9:13]))
	dur := time.Duration(binary.BigEndian.Uint64(h[13:21]))
	warm := time.Duration(binary.BigEndian.Uint64(h[21:29]))
	if err = validateProbe(size, dur, warm); err != nil {
		return err
	}
	return cfg.autoHub.serveControl(ctx, c, token, size, dur, warm)
}

func validateProbe(size int, dur, warm time.Duration) error {
	if size < 16 || size > maxProbePayload {
		return fmt.Errorf("auto probes require -buf 16..%d", maxProbePayload)
	}
	if dur <= 0 || dur > maxProbeWindow || warm < 0 || warm > maxProbeWindow {
		return fmt.Errorf("auto probes require 0 < -probe-t <= 60 and 0 <= -warmup <= 60 seconds")
	}
	return nil
}

// Receiver clock defines the window: immediately before READY through the
// completely consumed END frame, minus warmup. Drain time is included (never
// clamped to the sender's requested duration). Only complete verified frames
// whose read began after warmup count; framing/control bytes do not count.
func receiveProbe(c io.ReadWriter, size int, warm time.Duration) error {
	expected := makePayload(size)
	buf := make([]byte, size)
	start := time.Now()
	cutoff := start.Add(warm)
	if _, err := writeFull(c, []byte{'R'}); err != nil {
		return err
	}
	var count, received uint64
	var hdr [4]byte
	for {
		began := time.Now()
		if _, err := io.ReadFull(c, hdr[:]); err != nil {
			return err
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n == 0 {
			break
		}
		if int(n) != size {
			return fmt.Errorf("invalid probe frame length %d", n)
		}
		if _, err := io.ReadFull(c, buf); err != nil {
			return err
		}
		binary.BigEndian.PutUint64(expected[:8], count)
		if !bytes.Equal(buf, expected) {
			return fmt.Errorf("probe validation failed at frame %d", count)
		}
		count++
		if !began.Before(cutoff) {
			received += uint64(size)
		}
		if count%probeBatchFrames(size) == 0 {
			if _, err := writeFull(c, probeProgress(count)); err != nil {
				return err
			}
		}
	}
	elapsed := time.Since(cutoff)
	if elapsed <= 0 || received == 0 {
		return fmt.Errorf("no verified bytes after warmup")
	}
	report := make([]byte, 32)
	copy(report, "CPREPORT")
	binary.BigEndian.PutUint64(report[8:16], received)
	binary.BigEndian.PutUint64(report[16:24], uint64(elapsed))
	binary.BigEndian.PutUint64(report[24:32], count)
	if _, err := writeFull(c, report); err != nil {
		return err
	}
	return nil
}

// Resolve with the caller's context before gohome's non-context-aware resolver.
func resolveProbeAddr(ctx context.Context, addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
		port = ""
	}
	if net.ParseIP(host) != nil {
		return addr, nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", err
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("no addresses for %s", host)
	}
	if port == "" {
		return ips[0].IP.String(), nil
	}
	return net.JoinHostPort(ips[0].IP.String(), port), nil
}

// Dial stays on the caller goroutine. On cancellation Close is retried until
// Dial returns, covering a Close before gohome installs its internal cancel.
// The watchdog is always joined, never a detached per-timeout Dial goroutine.
func dialProbe(ctx context.Context, proto, addr string, cfg Config) (network.Conn, error) {
	addr, err := resolveProbeAddr(ctx, addr)
	if err != nil {
		return nil, err
	}
	if proto == "quic" {
		return dialAutoQUIC(ctx, addr)
	}
	f, err := newConn(proto, cfg)
	if err != nil {
		return nil, err
	}
	done, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-done:
			return
		case <-ctx.Done():
		}
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			_ = f.Close()
			select {
			case <-done:
				return
			case <-ticker.C:
			}
		}
	}()
	c, err := f.Dial(addr)
	close(done)
	<-joined
	if ctx.Err() != nil {
		if c != nil {
			_ = c.Close()
		}
		return nil, ctx.Err()
	}
	return c, err
}

func probeReceiverGoodput(parent context.Context, proto, addr string, size int, dur, warm time.Duration, cfg Config) probeResult {
	r := probeResult{Proto: proto, Addr: addr}
	if size <= 0 {
		size = defaultPayloadSize
	}
	if err := validateProbe(size, dur, warm); err != nil {
		r.Err = err
		return r
	}
	controlAddr, err := probeControlAddr(proto, addr, cfg.Server)
	if err != nil {
		r.Err = err
		return r
	}
	probeCtx, cancel := context.WithTimeout(parent, warm+dur+3*probeWait)
	defer cancel()
	control, err := (&net.Dialer{Timeout: probeWait}).DialContext(probeCtx, "tcp", controlAddr)
	if err != nil {
		r.Err = fmt.Errorf("auto v2 TCP control: %w", err)
		return r
	}
	defer control.Close()
	_ = control.SetDeadline(time.Now().Add(warm + dur + 3*probeWait))
	var once sync.Once
	stop := guardConn(probeCtx, func() { once.Do(func() { _ = control.Close() }) }, warm+dur+3*probeWait)
	defer stop()
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		r.Err = err
		return r
	}
	header := append(autoHeader('C', size, dur, warm), token[:]...)
	if _, err = writeFull(control, header); err != nil {
		r.Err = err
		return r
	}
	var hello [1]byte
	if _, err = io.ReadFull(control, hello[:]); err != nil || hello[0] != 'H' {
		r.Err = fmt.Errorf("auto v2 control handshake failed: %v", err)
		return r
	}
	dialCtx, dialCancel := context.WithTimeout(probeCtx, probeWait)
	c, err := dialProbe(dialCtx, proto, addr, cfg)
	dialCancel()
	if err != nil {
		r.Err = fmt.Errorf("probe dial: %w", err)
		return r
	}
	var dataOnce sync.Once
	closeConn := func() { dataOnce.Do(func() { _ = c.Close() }) }
	defer closeConn()
	dataStop := guardConn(probeCtx, closeConn, warm+dur+3*probeWait)
	defer dataStop()
	if _, err = writeFull(c, append(autoHeader('P', 0, 0, 0), token[:]...)); err != nil {
		r.Err = err
		return r
	}
	r.Err = sendProbe(probeCtx, c, control, size, dur, warm, &r)
	if probeCtx.Err() != nil {
		r.Err = probeCtx.Err()
	}
	return r
}

func probeControlAddr(proto, addr, base string) (string, error) {
	if base != "" {
		return base, nil
	}
	host, port, err := splitAddrHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("auto probes need the base TCP control address: %w", err)
	}
	offset, ok := autoPortOffset(proto)
	if !ok {
		return "", fmt.Errorf("no control port for %s", proto)
	}
	if port <= offset {
		return "", fmt.Errorf("invalid auto control port")
	}
	return net.JoinHostPort(host, fmt.Sprint(port-offset)), nil
}

func sendProbe(ctx context.Context, c io.Writer, control io.Reader, size int, dur, warm time.Duration, r *probeResult) error {
	var ack [1]byte
	if _, err := io.ReadFull(control, ack[:]); err != nil {
		return fmt.Errorf("auto v2 READY missing: %w", err)
	}
	if ack[0] != 'R' {
		return fmt.Errorf("incompatible auto server READY")
	}
	payload := makePayload(size)
	frame := make([]byte, 4+size)
	binary.BigEndian.PutUint32(frame[:4], uint32(size))
	end := time.Now().Add(warm + dur)
	var count uint64
	for time.Now().Before(end) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		binary.BigEndian.PutUint64(payload[:8], count)
		copy(frame[4:], payload)
		if _, err := writeFull(c, frame); err != nil {
			return fmt.Errorf("probe data: %w", err)
		}
		count++
		if count%probeBatchFrames(size) == 0 {
			progress := make([]byte, 9)
			if _, err := io.ReadFull(control, progress); err != nil {
				return fmt.Errorf("probe progress missing: %w", err)
			}
			if !bytes.Equal(progress, probeProgress(count)) {
				return fmt.Errorf("invalid probe progress")
			}
		}
	}
	if _, err := writeFull(c, make([]byte, 4)); err != nil {
		return err
	}
	report := make([]byte, 32)
	if _, err := io.ReadFull(control, report); err != nil {
		return fmt.Errorf("receiver report missing (auto v2 required): %w", err)
	}
	if err := applyProbeReport(r, report, count, size, warm+dur+2*probeWait); err != nil {
		return err
	}
	if _, err := io.ReadFull(control, ack[:]); err != nil {
		return fmt.Errorf("probe cleanup barrier: %w", err)
	}
	if ack[0] != 'D' {
		return fmt.Errorf("invalid probe cleanup barrier")
	}
	return nil
}

func applyProbeReport(r *probeResult, b []byte, sent uint64, size int, budget time.Duration) error {
	if len(b) != 32 || string(b[:8]) != "CPREPORT" {
		return fmt.Errorf("invalid receiver report")
	}
	received := binary.BigEndian.Uint64(b[8:16])
	elapsed := time.Duration(binary.BigEndian.Uint64(b[16:24]))
	frames := binary.BigEndian.Uint64(b[24:32])
	if frames != sent || received == 0 || received > sent*uint64(size) || received%uint64(size) != 0 || elapsed <= 0 || elapsed > budget {
		return fmt.Errorf("invalid receiver accounting: bytes=%d frames=%d/%d elapsed=%s", received, frames, sent, elapsed)
	}
	r.Bytes = int64(received)
	r.Elapsed = elapsed
	r.AvgBps = float64(received) / elapsed.Seconds()
	return nil
}
