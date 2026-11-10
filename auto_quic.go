package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"sync"

	"github.com/esrrhs/gohome/common"
	"github.com/esrrhs/gohome/network"
	"github.com/quic-go/quic-go"
)

// gohome's cached QUIC stream Close only closes its write side and retains the
// session for 30s. Auto probes instead own one native session each: Close
// cancels both stream directions and destroys the session, unblocking all IO.
// This adapter is auto-only; ordinary -p quic keeps gohome's original behavior.
type autoQUIC struct {
	session  *quic.Conn
	stream   *quic.Stream
	listener *quic.Listener
	ctx      context.Context
	cancel   context.CancelFunc
	once     sync.Once
}

func newAutoOrNormalConn(proto string, cfg Config) (network.Conn, error) {
	if cfg.autoTransport {
		return newAutoConn(proto, cfg)
	}
	return newConn(proto, cfg)
}
func newAutoConn(proto string, cfg Config) (network.Conn, error) {
	if proto == "quic" {
		return &autoQUIC{}, nil
	}
	return newConn(proto, cfg)
}
func dialAutoQUIC(ctx context.Context, addr string) (network.Conn, error) {
	session, err := quic.DialAddr(ctx, addr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"QuicConn"}}, nil)
	if err != nil {
		return nil, err
	}
	stream, err := session.OpenStreamSync(ctx)
	if err != nil {
		_ = session.CloseWithError(0, "dial failed")
		return nil, err
	}
	return &autoQUIC{session: session, stream: stream}, nil
}
func (c *autoQUIC) Name() string                { return "quic" }
func (c *autoQUIC) Info() string                { return "auto quic session" }
func (c *autoQUIC) Read(b []byte) (int, error)  { return c.stream.Read(b) }
func (c *autoQUIC) Write(b []byte) (int, error) { return c.stream.Write(b) }
func (c *autoQUIC) Close() error {
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
		if c.stream != nil {
			c.stream.CancelRead(0)
			c.stream.CancelWrite(0)
		}
		if c.session != nil {
			_ = c.session.CloseWithError(0, "auto closed")
		}
		if c.listener != nil {
			_ = c.listener.Close()
		}
	})
	return nil
}
func (c *autoQUIC) Dial(addr string) (network.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeWait)
	defer cancel()
	return dialAutoQUIC(ctx, addr)
}
func (c *autoQUIC) Listen(addr string) (network.Conn, error) {
	tlsCfg, err := common.GenerateTLSConfig("QuicConn")
	if err != nil {
		return nil, err
	}
	listener, err := quic.ListenAddr(addr, tlsCfg, nil)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &autoQUIC{listener: listener, ctx: ctx, cancel: cancel}, nil
}
func (c *autoQUIC) Accept() (network.Conn, error) {
	if c.listener == nil {
		return nil, fmt.Errorf("not a listener")
	}
	for {
		session, err := c.listener.Accept(c.ctx)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(c.ctx, probeWait)
		stream, err := session.AcceptStream(ctx)
		cancel()
		if err != nil {
			_ = session.CloseWithError(0, "no auto stream")
			continue
		}
		return &autoQUIC{session: session, stream: stream}, nil
	}
}
