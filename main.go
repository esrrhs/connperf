package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/esrrhs/connperf/version"
	"github.com/esrrhs/gohome/network"
)

const (
	defaultPayloadSize = 1024
	headerSize         = 16 // seq uint64 + send_ns uint64 (little-endian)
)

type Config struct {
	Server   string
	Listen   string
	Proto    string
	Write    bool
	Read     bool
	BufSize  int
	Duration time.Duration
	Unit     string // MBps (default), Mbps, Gbps
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
	unit := fs.String("unit", "MBps", "throughput unit: MBps (default), Mbps, or Gbps")

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

	normUnit, err := normalizeUnit(*unit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fs.Usage()
		os.Exit(1)
	}

	cfg := Config{
		Server:   *server,
		Listen:   *listen,
		Proto:    *proto,
		Write:    *write,
		Read:     *read,
		BufSize:  *bufSize,
		Duration: time.Duration(*durationSec) * time.Second,
		Unit:     normUnit,
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

func normalizeUnit(u string) (string, error) {
	u = strings.TrimSpace(u)
	switch u {
	case "", "MBps", "MB/s":
		return "MBps", nil
	case "Mbps", "Mbit/s":
		return "Mbps", nil
	case "Gbps", "Gbit/s":
		return "Gbps", nil
	}
	switch strings.ToLower(u) {
	case "mbps":
		// Lowercase mbps → megabits/sec (network convention).
		return "Mbps", nil
	case "gbps":
		return "Gbps", nil
	case "mb/s", "mib/s", "mibps":
		return "MBps", nil
	}
	return "", fmt.Errorf("unsupported -unit %q (want MBps, Mbps, or Gbps)", u)
}

// formatThroughput converts byte count over elapsed seconds into the selected unit.
// MBps uses binary MiB/s (1024^2); Mbps/Gbps use decimal bits (1e6 / 1e9).
func formatThroughput(bytes int64, sec float64, unit string) (value float64, label string) {
	if sec <= 0 {
		sec = 1
	}
	bps := float64(bytes) / sec
	switch unit {
	case "Mbps":
		return bps * 8 / 1e6, "Mbps"
	case "Gbps":
		return bps * 8 / 1e9, "Gbps"
	default:
		return bps / (1024 * 1024), "MB/s"
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


func formatMetricDuration(d time.Duration) string {
	d = d.Round(time.Microsecond)
	if d == 0 {
		return "0µs"
	}
	return d.String()
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// rttStats accumulates RTT samples for a reporting period.
type rttStats struct {
	mu    sync.Mutex
	count int64
	sum   int64 // nanoseconds
	min   int64
	max   int64
}

func (s *rttStats) add(ns int64) {
	if ns < 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.count == 0 || ns < s.min {
		s.min = ns
	}
	if s.count == 0 || ns > s.max {
		s.max = ns
	}
	s.sum += ns
	s.count++
}

func (s *rttStats) snapshotAndReset() (count int64, min, avg, max time.Duration, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.count == 0 {
		return 0, 0, 0, 0, false
	}
	count = s.count
	min = time.Duration(s.min)
	max = time.Duration(s.max)
	avg = time.Duration(s.sum / s.count)
	s.count, s.sum, s.min, s.max = 0, 0, 0, 0
	return count, min, avg, max, true
}

// jitterState tracks RFC3550-ish inter-arrival jitter from chunk arrivals.
type jitterState struct {
	mu        sync.Mutex
	jitter    int64 // smoothed, nanoseconds
	lastArr   time.Time
	lastDelta int64
	hasDelta  bool
	samples   int64
}

func (j *jitterState) onArrival(now time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.lastArr.IsZero() {
		delta := now.Sub(j.lastArr).Nanoseconds()
		if j.hasDelta {
			d := abs64(delta - j.lastDelta)
			// RFC 3550: J = J + (|D| - J) / 16
			j.jitter = j.jitter + (d-j.jitter)/16
			j.samples++
		}
		j.lastDelta = delta
		j.hasDelta = true
	}
	j.lastArr = now
}

func (j *jitterState) value() (time.Duration, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.samples == 0 {
		return 0, false
	}
	return time.Duration(j.jitter), true
}

const sentTrackCap = 4096

type sentTrack struct {
	mu   sync.Mutex
	times map[uint64]uint64 // seq -> send_ns
}

func newSentTrack() *sentTrack {
	return &sentTrack{times: make(map[uint64]uint64, 1024)}
}

func (t *sentTrack) put(seq, ns uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.times) >= sentTrackCap {
		// Drop an arbitrary old entry to bound memory.
		for k := range t.times {
			delete(t.times, k)
			break
		}
	}
	t.times[seq] = ns
}

func (t *sentTrack) take(seq uint64) (uint64, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ns, ok := t.times[seq]
	if ok {
		delete(t.times, seq)
	}
	return ns, ok
}

func show(ctx context.Context, c network.Conn, cfg Config) error {
	payloadSize := cfg.BufSize
	if payloadSize <= 0 {
		payloadSize = defaultPayloadSize
	}
	payload := makePayload(payloadSize)

	// Header/RTT path: both directions on this connection so peer can echo.
	useHeader := cfg.Write && cfg.Read && payloadSize >= headerSize
	bodyOff := 0
	if useHeader {
		bodyOff = headerSize
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var totalWriten atomic.Int64
	var totalReadn atomic.Int64
	var periodWriten atomic.Int64
	var periodReadn atomic.Int64
	var verifiedChunks atomic.Int64
	var writeSeq atomic.Uint64

	var periodRTT rttStats
	var totalRTT rttStats
	var recvJitter jitterState
	sent := newSentTrack()

	var echoMu sync.Mutex
	var echoSeq, echoNs uint64
	var echoValid bool

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

	if cfg.Read && !cfg.Write {
		fmt.Printf("note: RTT requires bidirectional (-read and -write on both ends); reporting throughput and receive jitter only\n")
	}

	startTime := time.Now()
	var wg sync.WaitGroup

	if cfg.Write {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wbuf := make([]byte, payloadSize)
			copy(wbuf, payload)

			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				if useHeader {
					echoMu.Lock()
					doEcho := echoValid
					es, en := echoSeq, echoNs
					if doEcho {
						echoValid = false
					}
					echoMu.Unlock()

					if doEcho {
						binary.LittleEndian.PutUint64(wbuf[0:8], es)
						binary.LittleEndian.PutUint64(wbuf[8:16], en)
					} else {
						seq := writeSeq.Add(1)
						ns := uint64(time.Now().UnixNano())
						binary.LittleEndian.PutUint64(wbuf[0:8], seq)
						binary.LittleEndian.PutUint64(wbuf[8:16], ns)
						sent.put(seq, ns)
					}
					// Body stays the deterministic pattern (header region excluded).
					copy(wbuf[bodyOff:], payload[bodyOff:])
				}

				n, err := writeFull(c, wbuf)
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
				arrive := time.Now()
				if n > 0 {
					totalReadn.Add(int64(n))
					periodReadn.Add(int64(n))
				}
				if err != nil {
					// Peer close / timeout mid-stream is normal; only full chunks are verified.
					return
				}

				recvJitter.onArrival(arrive)

				if useHeader {
					seq := binary.LittleEndian.Uint64(rbuf[0:8])
					ns := binary.LittleEndian.Uint64(rbuf[8:16])
					if _, ok := sent.take(seq); ok {
						rtt := arrive.UnixNano() - int64(ns)
						periodRTT.add(rtt)
						totalRTT.add(rtt)
					} else {
						// Peer's own stamp — queue for echo on our next write.
						echoMu.Lock()
						echoSeq, echoNs = seq, ns
						echoValid = true
						echoMu.Unlock()
					}
				}

				// Verify deterministic body; skip header when present.
				if !bytes.Equal(rbuf[bodyOff:], payload[bodyOff:]) {
					mismatch := bodyOff
					for i := bodyOff; i < payloadSize; i++ {
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
					wVal, wLbl := formatThroughput(wBytes, sec, cfg.Unit)
					line := fmt.Sprintf("write %.2f %s", wVal, wLbl)
					if useHeader {
						if _, rmin, ravg, rmax, ok := periodRTT.snapshotAndReset(); ok {
							line += fmt.Sprintf(" rtt min/avg/max %s/%s/%s",
								formatMetricDuration(rmin), formatMetricDuration(ravg), formatMetricDuration(rmax))
						}
					}
					fmt.Printf("%s %v\n", line, c.Info())
				}
				if cfg.Read {
					rVal, rLbl := formatThroughput(rBytes, sec, cfg.Unit)
					line := fmt.Sprintf("read %.2f %s", rVal, rLbl)
					if j, ok := recvJitter.value(); ok {
						line += fmt.Sprintf(" jitter %s", formatMetricDuration(j))
					}
					line += fmt.Sprintf(" (verified %d chunks)", verifiedChunks.Load())
					fmt.Printf("%s %v\n", line, c.Info())
				}
			}
		}
	}

	wg.Wait()

	totalElapsed := time.Since(startTime).Seconds()
	if totalElapsed > 0 {
		if cfg.Write {
			tw := totalWriten.Load()
			avgW, wLbl := formatThroughput(tw, totalElapsed, cfg.Unit)
			totalMB := float64(tw) / (1024 * 1024)
			line := fmt.Sprintf("summary: total write %.2f MB, avg speed %.2f %s", totalMB, avgW, wLbl)
			if useHeader {
				if _, rmin, ravg, rmax, ok := totalRTT.snapshotAndReset(); ok {
					line += fmt.Sprintf(", rtt min/avg/max %s/%s/%s",
						formatMetricDuration(rmin), formatMetricDuration(ravg), formatMetricDuration(rmax))
				}
			}
			fmt.Printf("%s [%v]\n", line, c.Info())
		}
		if cfg.Read {
			tr := totalReadn.Load()
			avgR, rLbl := formatThroughput(tr, totalElapsed, cfg.Unit)
			totalMB := float64(tr) / (1024 * 1024)
			line := fmt.Sprintf("summary: total read %.2f MB, avg speed %.2f %s, verified %d chunks",
				totalMB, avgR, rLbl, verifiedChunks.Load())
			if j, ok := recvJitter.value(); ok {
				line += fmt.Sprintf(", jitter %s", formatMetricDuration(j))
			}
			fmt.Printf("%s [%v]\n", line, c.Info())
		}
	}

	return failErr
}
