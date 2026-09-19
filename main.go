package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/esrrhs/connperf/version"
	"github.com/esrrhs/gohome/network"
)

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
	bufSizeKB := fs.Int("buf", 1024, "buffer size in KB")
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
		BufSize:  *bufSizeKB * 1024,
		Duration: time.Duration(*durationSec) * time.Second,
	}

	if cfg.BufSize <= 0 {
		cfg.BufSize = 1024 * 1024
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

		show(ctx, client, cfg)
		return nil
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

		fmt.Printf("listening on %s (%s)...\n", cfg.Listen, cfg.Proto)

		var wg sync.WaitGroup
		for {
			sonny, err := server.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					wg.Wait()
					return nil
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

				show(connCtx, conn, cfg)
			}(sonny)
		}
	}

	return nil
}

func show(ctx context.Context, c network.Conn, cfg Config) {
	bufSize := cfg.BufSize
	if bufSize <= 0 {
		bufSize = 1024 * 1024
	}

	var totalWriten atomic.Int64
	var totalReadn atomic.Int64
	var periodWriten atomic.Int64
	var periodReadn atomic.Int64

	startTime := time.Now()
	var wg sync.WaitGroup

	// Writer routine
	if cfg.Write {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wbuf := make([]byte, bufSize)
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				n, err := c.Write(wbuf)
				if err != nil {
					return
				}
				totalWriten.Add(int64(n))
				periodWriten.Add(int64(n))
			}
		}()
	}

	// Reader routine
	if cfg.Read {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rbuf := make([]byte, bufSize)
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				n, err := c.Read(rbuf)
				if err != nil {
					return
				}
				totalReadn.Add(int64(n))
				periodReadn.Add(int64(n))
			}
		}()
	}

	// Metrics reporter ticker
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
					fmt.Printf("read %.2f MB/s %v\n", rMBps, c.Info())
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
			fmt.Printf("summary: total read %.2f MB, avg speed %.2f MB/s [%v]\n",
				float64(tr)/(1024*1024), avgR, c.Info())
		}
	}
}
