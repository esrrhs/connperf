package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/esrrhs/connperf/version"
	"github.com/esrrhs/gohome/network"
)

const defaultPayloadSize = 1024

type Config struct {
	Server   string
	Listen   string
	Proto    string
	Write    bool
	Read     bool
	BufSize  int
	Duration time.Duration
}

func main() {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	server := fs.String("s", "", "server addr")
	listen := fs.String("l", "", "listen addr")
	proto := fs.String("p", "tcp", fmt.Sprintf("proto %v", network.SupportReliableProtos()))
	write := fs.Bool("write", false, "write")
	read := fs.Bool("read", false, "read")
	showVer := fs.Bool("v", false, "show version")
	showVersion := fs.Bool("version", false, "show full version")
	bufSize := fs.Int("buf", defaultPayloadSize, "fixed payload size in bytes (sent/verified each round)")
	durationSec := fs.Int("t", 0, "test duration in seconds (0 for indefinite)")

	if err := fs.Parse(os.Args[1:]); err != nil {
		fs.Usage()
		os.Exit(1)
	}

	if *showVersion {
		fmt.Println(version.Full())
		return
	}
	if *showVer {
		fmt.Println(version.Short())
		return
	}

	if *server == "" && *listen == "" {
		fs.Usage()
		return
	}

	if !*write && !*read {
		fs.Usage()
		return
	}

	if !network.HasReliableProto(*proto) {
		fmt.Printf("unsupported protocol: %s, supported: %v\n", *proto, network.SupportReliableProtos())
		fs.Usage()
		return
	}

	cfg := Config{
		Server:   *server,
		Listen:   *listen,
		Proto:    *proto,
		Write:    *write,
		Read:     *read,
		BufSize:  *bufSize,
		Duration: time.Duration(*durationSec) * time.Second,
	}

	if cfg.BufSize <= 0 {
		cfg.BufSize = defaultPayloadSize
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if cfg.Duration > 0 {
		var dCancel context.CancelFunc
		ctx, dCancel = context.WithTimeout(ctx, cfg.Duration)
		defer dCancel()
	}

	if err := run(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg Config) error {
	c, err := network.NewConn(cfg.Proto)
	if err != nil {
		return fmt.Errorf("create conn %s failed: %w", cfg.Proto, err)
	}

	if cfg.Server != "" {
		client, err := c.Dial(cfg.Server)
		if err != nil {
			return fmt.Errorf("dial %s %s failed: %w", cfg.Proto, cfg.Server, err)
		}
		defer client.Close()

		go func() {
			<-ctx.Done()
			client.Close()
		}()

		return show(ctx, client, cfg)
	}

	if cfg.Listen != "" {
		server, err := c.Listen(cfg.Listen)
		if err != nil {
			return fmt.Errorf("listen %s %s failed: %w", cfg.Proto, cfg.Listen, err)
		}
		defer server.Close()

		go func() {
			<-ctx.Done()
			server.Close()
		}()

		fmt.Printf("listening on %s (%s), payload %d bytes...\n", cfg.Listen, cfg.Proto, cfg.BufSize)

		var (
			wg       sync.WaitGroup
			errOnce  sync.Once
			firstErr error
		)
		setErr := func(e error) {
			if e == nil {
				return
			}
			errOnce.Do(func() {
				firstErr = e
			})
		}

		for {
			sonny, err := server.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					wg.Wait()
					return firstErr
				default:
				}
				return fmt.Errorf("accept failed: %w", err)
			}

			wg.Add(1)
			go func(conn network.Conn) {
				defer wg.Done()
				defer conn.Close()

				connCtx, connCancel := context.WithCancel(ctx)
				defer connCancel()

				go func() {
					<-connCtx.Done()
					conn.Close()
				}()

				setErr(show(connCtx, conn, cfg))
			}(sonny)
		}
	}

	return nil
}

// makePayload builds a deterministic fixed pattern used by both ends.
func makePayload(size int) []byte {
	p := make([]byte, size)
	for i := range p {
		p[i] = byte(i % 256)
	}
	return p
}

func writeFull(c network.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := c.Write(buf[total:])
		if n > 0 {
			total += n
		}
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrUnexpectedEOF
		}
	}
	return total, nil
}

func readFull(c network.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := c.Read(buf[total:])
		if n > 0 {
			total += n
		}
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrUnexpectedEOF
		}
	}
	return total, nil
}

func show(ctx context.Context, c network.Conn, cfg Config) error {
	payloadSize := cfg.BufSize
	if payloadSize <= 0 {
		payloadSize = defaultPayloadSize
	}
	payload := makePayload(payloadSize)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var totalWriten atomic.Int64
	var totalReadn atomic.Int64
	var periodWriten atomic.Int64
	var periodReadn atomic.Int64
	var verifiedChunks atomic.Int64

	var failOnce sync.Once
	var failErr error
	fail := func(err error) {
		if err == nil {
			return
		}
		failOnce.Do(func() {
			failErr = err
			cancel()
			_ = c.Close()
		})
	}

	startTime := time.Now()
	var wg sync.WaitGroup

	if cfg.Write {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				n, err := writeFull(c, payload)
				if n > 0 {
					totalWriten.Add(int64(n))
					periodWriten.Add(int64(n))
				}
				if err != nil {
					return
				}
			}
		}()
	}

	if cfg.Read {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rbuf := make([]byte, payloadSize)
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				n, err := readFull(c, rbuf)
				if n > 0 {
					totalReadn.Add(int64(n))
					periodReadn.Add(int64(n))
				}
				if err != nil {
					// Peer close / timeout mid-stream is normal; only full chunks are verified.
					return
				}

				if !bytes.Equal(rbuf, payload) {
					mismatch := 0
					for i := 0; i < payloadSize; i++ {
						if rbuf[i] != payload[i] {
							mismatch = i
							break
						}
					}
					fail(fmt.Errorf("payload mismatch at byte %d: got 0x%02x want 0x%02x",
						mismatch, rbuf[mismatch], payload[mismatch]))
					return
				}
				verifiedChunks.Add(1)
			}
		}()
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	lastTick := time.Now()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case now := <-ticker.C:
			elapse := now.Sub(lastTick)
			lastTick = now

			wBytes := periodWriten.Swap(0)
			rBytes := periodReadn.Swap(0)

			sec := float64(elapse) / float64(time.Second)
			if sec > 0 {
				if cfg.Write {
					wMBps := (float64(wBytes) / (1024 * 1024)) / sec
					fmt.Printf("write %.2f MB/s %v\n", wMBps, c.Info())
				}
				if cfg.Read {
					rMBps := (float64(rBytes) / (1024 * 1024)) / sec
					fmt.Printf("read %.2f MB/s (verified %d chunks) %v\n",
						rMBps, verifiedChunks.Load(), c.Info())
				}
			}
		}
	}

	wg.Wait()

	totalElapsed := time.Since(startTime).Seconds()
	if totalElapsed > 0 {
		if cfg.Write {
			tw := totalWriten.Load()
			avgW := (float64(tw) / (1024 * 1024)) / totalElapsed
			fmt.Printf("summary: total write %.2f MB, avg speed %.2f MB/s [%v]\n",
				float64(tw)/(1024*1024), avgW, c.Info())
		}
		if cfg.Read {
			tr := totalReadn.Load()
			avgR := (float64(tr) / (1024 * 1024)) / totalElapsed
			fmt.Printf("summary: total read %.2f MB, avg speed %.2f MB/s, verified %d chunks [%v]\n",
				float64(tr)/(1024*1024), avgR, verifiedChunks.Load(), c.Info())
		}
	}

	return failErr
}
