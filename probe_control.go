package main

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/esrrhs/gohome/network"
)

// Reports and progress use the base-port TCP control connection, not the
// measured transport. This avoids assuming UDP-based transports implement EOF
// or flush pending writes on Close. One unguessable token binds one data
// connection to one bounded control session. Tokens are single-use.
type probeHub struct {
	mu       sync.Mutex
	sessions map[[16]byte]*probeSession
}
type probeSession struct {
	ctx     context.Context
	control io.Writer
	size    int
	warm    time.Duration
	claimed bool       // protected by hub.mu
	done    chan error // sent only after the data connection's Close has joined
}

func (h *probeHub) claim(token [16]byte) (*probeSession, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[token]
	if s == nil || s.claimed || s.ctx.Err() != nil {
		return nil, fmt.Errorf("unknown, expired or reused probe token")
	}
	s.claimed = true
	return s, nil
}
func (h *probeHub) serveControl(ctx context.Context, c network.Conn, token [16]byte, size int, dur, warm time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, warm+dur+3*probeWait)
	defer cancel()
	// The client sends no further control bytes. EOF (including a failed data
	// dial) cancels the session immediately instead of retaining it until TTL.
	peerDone := make(chan struct{})
	go func() { defer close(peerDone); var b [1]byte; _, _ = c.Read(b[:]); cancel() }()
	defer func() { _ = c.Close(); <-peerDone }()
	s := &probeSession{ctx: ctx, control: c, size: size, warm: warm, done: make(chan error, 1)}
	h.mu.Lock()
	if len(h.sessions) >= 128 || h.sessions[token] != nil {
		h.mu.Unlock()
		return fmt.Errorf("probe control capacity or duplicate token")
	}
	h.sessions[token] = s
	h.mu.Unlock()
	defer func() {
		cancel()
		_ = c.Close()
		h.mu.Lock()
		delete(h.sessions, token)
		claimed := s.claimed
		h.mu.Unlock()
		// If canceled before a report, Close on the data side is driven by ctx.
		if claimed {
			<-s.done
		}
	}()
	var once sync.Once
	stop := guardConn(ctx, func() { once.Do(func() { _ = c.Close() }) }, warm+dur+3*probeWait)
	defer stop()
	if _, err := writeFull(c, []byte{'H'}); err != nil {
		return err
	}
	// Keep the result available for the deferred join as well.
	select {
	case err := <-s.done:
		s.done <- err
		if err != nil {
			return fmt.Errorf("probe data: %w", err)
		}
		_, err = writeFull(c, []byte{'D'})
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (h *probeHub) serveData(c network.Conn, token [16]byte) error {
	s, err := h.claim(token)
	if err != nil {
		return err
	}
	var once sync.Once
	closeConn := func() { once.Do(func() { _ = c.Close() }) }
	stop := guardConn(s.ctx, closeConn, 2*maxProbeWindow+3*probeWait)
	// A data handler is entirely synchronous; no detached reader/writer. Publish
	// completion only after transport Close and its cancellation watcher join.
	err = receiveProbe(struct {
		io.Reader
		io.Writer
	}{c, s.control}, s.size, s.warm)
	closeConn()
	stop()
	s.done <- err
	return err
}
