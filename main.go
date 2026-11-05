package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/esrrhs/connperf/version"
	"github.com/esrrhs/gohome/loggo"
	"github.com/esrrhs/gohome/network"
)

const (
	defaultPayloadSize = 1024
	headerSize         = 16 // seq uint64 + send_ns uint64 (little-endian)
)

type Config struct {
	Server    string
	Listen    string
	Proto     string
	Write     bool
	Read      bool
	BufSize   int
	Duration  time.Duration
	Unit      string // MBps (default), Mbps, Gbps
	Conns     int    // client parallel connections (-n), default 1
	Warmup    time.Duration
	MaxConn   int           // server accept limit (0 = unlimited)
	ProbeTime time.Duration // auto-mode per-protocol probe window (-probe-t)
	ProbeOnly bool          // auto-mode: print comparison table and exit
	AutoRICMP     bool          // auto-mode: include ricmp (excluded by default)
	AutoRDNS      bool          // auto-mode: include rdns (requires -rdnsdomain; excluded by default)
	RdnsDomain    string        // RDNS base tunnel domain (required when proto is rdns)
	RdnsPSK       string        // RDNS underlay AES-GCM PSK payload encryption
	RdnsQueryType string        // RDNS query type: TXT (default) or NULL
	RdnsUpstream  string        // RDNS upstream DNS server
	Key           string        // obfuscation/encryption key
	Debug         bool
}

func main() {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	server := fs.String("s", "", "server addr")
	listen := fs.String("l", "", "listen addr")
	proto := fs.String("p", "tcp", fmt.Sprintf("proto %v or auto", network.SupportReliableProtos()))
	write := fs.Bool("write", false, "write")
	read := fs.Bool("read", false, "read")
	showVer := fs.Bool("v", false, "show version")
	showVersion := fs.Bool("version", false, "show full version")
	bufSize := fs.Int("buf", defaultPayloadSize, "fixed payload size in bytes (sent/verified each round)")
	durationSec := fs.Int("t", 0, "test duration in seconds (0 for indefinite)")
	unit := fs.String("unit", "MBps", "throughput unit: MBps (default), Mbps, or Gbps")
	conns := fs.Int("n", 1, "client parallel connections (default 1)")
	warmupSec := fs.Int("warmup", 0, "seconds of traffic ignored in stats (discard counters)")
	maxConn := fs.Int("maxconn", 0, "server max accepted connections (0 = unlimited)")
	probeSec := fs.Int("probe-t", 2, "auto mode: per-protocol probe duration in seconds (default 2)")
	probeOnly := fs.Bool("probe-only", false, "auto mode: print probe comparison table and exit without full run")
	autoRICMP := fs.Bool("auto-ricmp", false, "auto mode: include ricmp (needs root/CAP_NET_RAW; excluded by default)")
	autoRDNS := fs.Bool("auto-rdns", false, "auto mode: include rdns (requires -rdnsdomain; excluded by default)")
	rdnsDomain := fs.String("rdnsdomain", "", "RDNS base tunnel domain (e.g. rdns.bwh.esrrhs.xyz); required for rdns proto")
	rdnsPSK := fs.String("rdnspsk", "", "RDNS underlay AES-GCM PSK payload encryption")
	rdnsQueryType := fs.String("rdnsquerytype", "TXT", "RDNS query type: TXT (default) or NULL")
	rdnsUpstream := fs.String("rdnsupstream", "", "RDNS upstream DNS server for non-tunnel queries")
	key := fs.String("key", "", "obfuscation/encryption key")
	debug := fs.Bool("debug", false, "enable debug logging and internal stat printing")

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

	if !isAutoProto(*proto) && !network.HasReliableProto(*proto) {
		fmt.Printf("unsupported protocol: %s, supported: %v or auto\n", *proto, network.SupportReliableProtos())
		fs.Usage()
		return
	}

	normUnit, err := normalizeUnit(*unit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fs.Usage()
		os.Exit(1)
	}

	n := *conns
	if n < 1 {
		fmt.Fprintf(os.Stderr, "error: -n must be >= 1\n")
		fs.Usage()
		os.Exit(1)
	}
	if *warmupSec < 0 {
		fmt.Fprintf(os.Stderr, "error: -warmup must be >= 0\n")
		fs.Usage()
		os.Exit(1)
	}
	if *maxConn < 0 {
		fmt.Fprintf(os.Stderr, "error: -maxconn must be >= 0\n")
		fs.Usage()
		os.Exit(1)
	}
	if *probeSec < 1 {
		fmt.Fprintf(os.Stderr, "error: -probe-t must be >= 1\n")
		fs.Usage()
		os.Exit(1)
	}
	if (*probeOnly || *autoRICMP || *autoRDNS) && !isAutoProto(*proto) {
		fmt.Fprintf(os.Stderr, "error: -probe-only / -auto-ricmp / -auto-rdns require -p auto\n")
		fs.Usage()
		os.Exit(1)
	}
	if *proto == "rdns" && *rdnsDomain == "" {
		fmt.Fprintf(os.Stderr, "error: -rdnsdomain is required when using rdns proto\n")
		fs.Usage()
		os.Exit(1)
	}
	if *autoRDNS && *rdnsDomain == "" {
		fmt.Fprintf(os.Stderr, "error: -auto-rdns requires -rdnsdomain\n")
		fs.Usage()
		os.Exit(1)
	}

	cfg := Config{
		Server:        *server,
		Listen:        *listen,
		Proto:         *proto,
		Write:         *write,
		Read:          *read,
		BufSize:       *bufSize,
		Duration:      time.Duration(*durationSec) * time.Second,
		Unit:          normUnit,
		Conns:         n,
		Warmup:        time.Duration(*warmupSec) * time.Second,
		MaxConn:       *maxConn,
		ProbeTime:     time.Duration(*probeSec) * time.Second,
		ProbeOnly:     *probeOnly,
		AutoRICMP:     *autoRICMP,
		AutoRDNS:      *autoRDNS,
		RdnsDomain:    *rdnsDomain,
		RdnsPSK:       *rdnsPSK,
		RdnsQueryType: *rdnsQueryType,
		RdnsUpstream:  *rdnsUpstream,
		Key:           *key,
		Debug:         *debug,
	}

	if *debug {
		loggo.Ini(loggo.Config{
			Level:      loggo.LEVEL_DEBUG,
			Prefix:     "connperf",
			NoLogFile:  true,
			NoPrint:    false,
			NoLogColor: false,
		})
	}

	if cfg.BufSize <= 0 {
		cfg.BufSize = defaultPayloadSize
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Auto client probes each protocol for -probe-t (plus warmup) before the
	// full run; apply -t only to non-auto runs here. Auto full-run duration is
	// enforced inside runAutoClient.
	if cfg.Duration > 0 && !isAutoProto(cfg.Proto) {
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

func newConn(proto string, cfg Config) (network.Conn, error) {
	c, err := network.NewConn(proto)
	if err != nil {
		return nil, err
	}
	if c.Name() == "rdns" {
		if rc, ok := c.(*network.RdnsConn); ok {
			rcCfg := rc.GetConfig()
			if cfg.RdnsDomain != "" {
				rcCfg.Domain = cfg.RdnsDomain
			}
			if cfg.RdnsPSK != "" {
				rcCfg.PSK = cfg.RdnsPSK
			}
			if cfg.RdnsQueryType != "" {
				rcCfg.QueryType = cfg.RdnsQueryType
			}
			if cfg.RdnsUpstream != "" {
				rcCfg.Upstream = cfg.RdnsUpstream
			}
			rc.SetConfig(rcCfg)
		}
	}
	if rc, ok := c.(*network.RudpConn); ok {
		rcCfg := rc.GetConfig()
		if cfg.Key != "" {
			rcCfg.Key = cfg.Key
		}
		if cfg.Debug {
			rcCfg.Stat = 1
		}
		rc.SetConfig(rcCfg)
	}
	return c, nil
}

func run(ctx context.Context, cfg Config) error {
	if isAutoProto(cfg.Proto) {
		if cfg.Server != "" {
			return runAutoClient(ctx, cfg)
		}
		if cfg.Listen != "" {
			return runAutoServer(ctx, cfg)
		}
		return nil
	}

	c, err := newConn(cfg.Proto, cfg)
	if err != nil {
		return fmt.Errorf("create conn %s failed: %w", cfg.Proto, err)
	}

	if cfg.Server != "" {
		return runClient(ctx, c, cfg)
	}

	if cfg.Listen != "" {
		return runServer(ctx, c, cfg)
	}

	return nil
}

func runClient(ctx context.Context, factory network.Conn, cfg Config) error {
	n := cfg.Conns
	if n < 1 {
		n = 1
	}

	type dialResult struct {
		conn network.Conn
		err  error
	}
	results := make([]dialResult, n)
	var dialWG sync.WaitGroup
	for i := 0; i < n; i++ {
		dialWG.Add(1)
		go func(i int) {
			defer dialWG.Done()
			// Fresh factory per dial so concurrent Dial on shared state cannot race.
			f, err := newConn(cfg.Proto, cfg)
			if err != nil {
				results[i].err = err
				return
			}
			conn, err := f.Dial(cfg.Server)
			results[i] = dialResult{conn: conn, err: err}
		}(i)
	}
	dialWG.Wait()
	_ = factory // proto already validated; per-dial factories used above

	conns := make([]network.Conn, 0, n)
	var dialErrs []error
	for i, r := range results {
		if r.err != nil {
			dialErrs = append(dialErrs, fmt.Errorf("conn %d: %w", i, r.err))
			continue
		}
		conns = append(conns, r.conn)
	}
	if len(dialErrs) > 0 {
		for _, c := range conns {
			_ = c.Close()
		}
		return fmt.Errorf("dial %s %s failed: %w", cfg.Proto, cfg.Server, errors.Join(dialErrs...))
	}

	for _, conn := range conns {
		c := conn
		go func() {
			<-ctx.Done()
			_ = c.Close()
		}()
	}

	if n > 1 {
		fmt.Printf("opened %d connections to %s (%s)\n", n, cfg.Server, cfg.Proto)
	}

	return showMany(ctx, conns, cfg)
}

func runServer(ctx context.Context, factory network.Conn, cfg Config) error {
	server, err := factory.Listen(cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %s %s failed: %w", cfg.Proto, cfg.Listen, err)
	}
	defer server.Close()

	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()

	maxNote := "unlimited"
	if cfg.MaxConn > 0 {
		maxNote = fmt.Sprintf("%d", cfg.MaxConn)
	}
	fmt.Printf("listening on %s (%s), payload %d bytes, maxconn %s...\n",
		cfg.Listen, cfg.Proto, cfg.BufSize, maxNote)

	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
		active   atomic.Int64
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

		if cfg.MaxConn > 0 && int(active.Load()) >= cfg.MaxConn {
			_ = sonny.Close()
			continue
		}

		active.Add(1)
		wg.Add(1)
		go func(conn network.Conn) {
			defer wg.Done()
			defer active.Add(-1)
			defer conn.Close()

			connCtx, connCancel := context.WithCancel(ctx)
			defer connCancel()

			go func() {
				<-connCtx.Done()
				_ = conn.Close()
			}()

			setErr(showMany(connCtx, []network.Conn{conn}, cfg))
		}(sonny)
	}
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

// isBenignClose reports errors expected on shutdown / peer close.
func isBenignClose(err error, ctx context.Context) bool {
	if err == nil {
		return true
	}
	if ctx.Err() != nil {
		return true
	}
	if errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "closed") ||
		strings.Contains(msg, "use of closed") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") {
		return true
	}
	return false
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

// rttSampleCap bounds memory/CPU for percentile computation under high
// sample rates. Exact min/avg/max still use every accepted sample; p50/p90/p99
// are estimated from a uniform reservoir of at most this many nanosecond values.
const rttSampleCap = 8192

// rttStats accumulates RTT samples for a reporting period.
type rttStats struct {
	mu      sync.Mutex
	count   int64
	sum     int64 // nanoseconds
	min     int64
	max     int64
	samples []int64 // reservoir for percentiles (nanoseconds)
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
	if len(s.samples) < rttSampleCap {
		s.samples = append(s.samples, ns)
		return
	}
	// Algorithm R: keep a uniform reservoir of size rttSampleCap.
	j := rand.Int64N(s.count)
	if j < int64(rttSampleCap) {
		s.samples[j] = ns
	}
}

// percentileDuration returns the p-th percentile (0..100) of sorted nanosecond
// samples using linear interpolation between nearest ranks.
func percentileDuration(sorted []int64, p float64) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 || p <= 0 {
		return time.Duration(sorted[0])
	}
	if p >= 100 {
		return time.Duration(sorted[n-1])
	}
	rank := p / 100 * float64(n-1)
	lo := int(rank)
	hi := lo + 1
	if hi >= n {
		return time.Duration(sorted[n-1])
	}
	frac := rank - float64(lo)
	v := float64(sorted[lo])*(1-frac) + float64(sorted[hi])*frac
	return time.Duration(v)
}

func (s *rttStats) snapshotAndReset() (count int64, min, avg, max, p50, p90, p99 time.Duration, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.count == 0 {
		return 0, 0, 0, 0, 0, 0, 0, false
	}
	count = s.count
	min = time.Duration(s.min)
	max = time.Duration(s.max)
	avg = time.Duration(s.sum / s.count)
	if n := len(s.samples); n > 0 {
		sorted := append([]int64(nil), s.samples...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		p50 = percentileDuration(sorted, 50)
		p90 = percentileDuration(sorted, 90)
		p99 = percentileDuration(sorted, 99)
	}
	s.count, s.sum, s.min, s.max = 0, 0, 0, 0
	s.samples = s.samples[:0]
	return count, min, avg, max, p50, p90, p99, true
}

func (s *rttStats) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count, s.sum, s.min, s.max = 0, 0, 0, 0
	s.samples = s.samples[:0]
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

func (j *jitterState) reset() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.jitter = 0
	j.lastArr = time.Time{}
	j.lastDelta = 0
	j.hasDelta = false
	j.samples = 0
}

const sentTrackCap = 4096

type sentTrack struct {
	mu    sync.Mutex
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

// sharedIO holds counters shared across one or more connections.
type sharedIO struct {
	totalWriten    atomic.Int64
	totalReadn     atomic.Int64
	periodWriten   atomic.Int64
	periodReadn    atomic.Int64
	verifiedChunks atomic.Int64
	periodRTT      rttStats
	totalRTT       rttStats
	recvJitter     jitterState
}

// showMany runs read/write pumps on conns and aggregates throughput.
// Bidirectional mode uses one reader + one writer goroutine per Conn (standard
// net.Conn pattern). gohome FrameMgr separates send/recv locks; TCP delegates
// to net.TCPConn which allows concurrent Read and Write.
func showMany(ctx context.Context, conns []network.Conn, cfg Config) error {
	if len(conns) == 0 {
		return fmt.Errorf("no connections")
	}

	payloadSize := cfg.BufSize
	if payloadSize <= 0 {
		payloadSize = defaultPayloadSize
	}
	payload := makePayload(payloadSize)

	// Header/RTT path: both directions so peer can echo.
	// Fixed-size framing (writeFull/readFull of payloadSize) keeps chunks aligned;
	// body verification catches misaligned streams.
	useHeader := cfg.Write && cfg.Read && payloadSize >= headerSize
	bodyOff := 0
	if useHeader {
		bodyOff = headerSize
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stats := &sharedIO{}

	var failOnce sync.Once
	var failErr error
	var closeOnce sync.Once
	fail := func(err error) {
		if err == nil {
			return
		}
		failOnce.Do(func() {
			failErr = err
			cancel()
			closeOnce.Do(func() {
				for _, c := range conns {
					_ = c.Close()
				}
			})
		})
	}

	if cfg.Read && !cfg.Write {
		fmt.Printf("note: RTT requires bidirectional (-read and -write on both ends); reporting throughput and receive jitter only\n")
	}

	startTime := time.Now()
	statsStart := startTime
	if cfg.Warmup > 0 {
		statsStart = startTime.Add(cfg.Warmup)
	}
	var wg sync.WaitGroup

	for _, conn := range conns {
		c := conn
		startConnPumps(ctx, c, cfg, payload, payloadSize, bodyOff, useHeader, stats, &wg, fail)
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	lastTick := time.Now()
	warmedUp := cfg.Warmup <= 0
	infoLabel := connInfoLabel(conns)

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case now := <-ticker.C:
			elapse := now.Sub(lastTick)
			lastTick = now

			wBytes := stats.periodWriten.Swap(0)
			rBytes := stats.periodReadn.Swap(0)

			if !warmedUp {
				if now.Before(statsStart) {
					// Discard period samples collected during warmup.
					_ = wBytes
					_ = rBytes
					stats.periodRTT.reset()
					continue
				}
				// First tick at/after warmup end: reset cumulative counters.
				stats.totalWriten.Store(0)
				stats.totalReadn.Store(0)
				stats.verifiedChunks.Store(0)
				stats.totalRTT.reset()
				stats.recvJitter.reset()
				stats.periodRTT.reset()
				statsStart = now
				warmedUp = true
				continue
			}

			sec := float64(elapse) / float64(time.Second)
			if sec > 0 {
				printPeriod(cfg, useHeader, stats, wBytes, rBytes, sec, infoLabel)
			}
		}
	}

	wg.Wait()

	totalElapsed := time.Since(statsStart).Seconds()
	if warmedUp && totalElapsed > 0 {
		printSummary(cfg, useHeader, stats, totalElapsed, infoLabel)
	}

	return failErr
}

func connInfoLabel(conns []network.Conn) string {
	if len(conns) == 1 {
		return conns[0].Info()
	}
	return fmt.Sprintf("%d conns, e.g. %s", len(conns), conns[0].Info())
}

func printPeriod(cfg Config, useHeader bool, stats *sharedIO, wBytes, rBytes int64, sec float64, info string) {
	if cfg.Write {
		wVal, wLbl := formatThroughput(wBytes, sec, cfg.Unit)
		line := fmt.Sprintf("write %.2f %s", wVal, wLbl)
		if useHeader {
			if _, rmin, ravg, rmax, p50, p90, p99, ok := stats.periodRTT.snapshotAndReset(); ok {
				line += fmt.Sprintf(" rtt min/avg/max/p50/p90/p99 %s/%s/%s/%s/%s/%s",
					formatMetricDuration(rmin), formatMetricDuration(ravg), formatMetricDuration(rmax),
					formatMetricDuration(p50), formatMetricDuration(p90), formatMetricDuration(p99))
			}
		}
		fmt.Printf("%s %v\n", line, info)
	}
	if cfg.Read {
		rVal, rLbl := formatThroughput(rBytes, sec, cfg.Unit)
		line := fmt.Sprintf("read %.2f %s", rVal, rLbl)
		if j, ok := stats.recvJitter.value(); ok {
			line += fmt.Sprintf(" jitter %s", formatMetricDuration(j))
		}
		line += fmt.Sprintf(" (verified %d chunks)", stats.verifiedChunks.Load())
		fmt.Printf("%s %v\n", line, info)
	}
}

func printSummary(cfg Config, useHeader bool, stats *sharedIO, totalElapsed float64, info string) {
	if cfg.Write {
		tw := stats.totalWriten.Load()
		avgW, wLbl := formatThroughput(tw, totalElapsed, cfg.Unit)
		totalMB := float64(tw) / (1024 * 1024)
		line := fmt.Sprintf("summary: total write %.2f MB, avg speed %.2f %s", totalMB, avgW, wLbl)
		if useHeader {
			if _, rmin, ravg, rmax, p50, p90, p99, ok := stats.totalRTT.snapshotAndReset(); ok {
				line += fmt.Sprintf(", rtt min/avg/max/p50/p90/p99 %s/%s/%s/%s/%s/%s",
					formatMetricDuration(rmin), formatMetricDuration(ravg), formatMetricDuration(rmax),
					formatMetricDuration(p50), formatMetricDuration(p90), formatMetricDuration(p99))
			}
		}
		fmt.Printf("%s [%v]\n", line, info)
	}
	if cfg.Read {
		tr := stats.totalReadn.Load()
		avgR, rLbl := formatThroughput(tr, totalElapsed, cfg.Unit)
		totalMB := float64(tr) / (1024 * 1024)
		line := fmt.Sprintf("summary: total read %.2f MB, avg speed %.2f %s, verified %d chunks",
			totalMB, avgR, rLbl, stats.verifiedChunks.Load())
		if j, ok := stats.recvJitter.value(); ok {
			line += fmt.Sprintf(", jitter %s", formatMetricDuration(j))
		}
		fmt.Printf("%s [%v]\n", line, info)
	}
}

func startConnPumps(
	ctx context.Context,
	c network.Conn,
	cfg Config,
	payload []byte,
	payloadSize, bodyOff int,
	useHeader bool,
	stats *sharedIO,
	wg *sync.WaitGroup,
	fail func(error),
) {
	var writeSeq atomic.Uint64
	sent := newSentTrack()

	var echoMu sync.Mutex
	var echoSeq, echoNs uint64
	var echoValid bool

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
					stats.totalWriten.Add(int64(n))
					stats.periodWriten.Add(int64(n))
				}
				if err != nil {
					if !isBenignClose(err, ctx) {
						fail(fmt.Errorf("write: %w", err))
					}
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
					stats.totalReadn.Add(int64(n))
					stats.periodReadn.Add(int64(n))
				}
				if err != nil {
					// Short/partial chunk at shutdown is expected; unexpected
					// mid-stream errors (and unexpected EOF with 0 bytes after
					// a verified stream) surface when not benign.
					if n > 0 && n < payloadSize && !isBenignClose(err, ctx) {
						fail(fmt.Errorf("read incomplete chunk (%d/%d): %w", n, payloadSize, err))
					} else if !isBenignClose(err, ctx) {
						fail(fmt.Errorf("read: %w", err))
					}
					return
				}

				stats.recvJitter.onArrival(arrive)

				if useHeader {
					seq := binary.LittleEndian.Uint64(rbuf[0:8])
					ns := binary.LittleEndian.Uint64(rbuf[8:16])
					if _, ok := sent.take(seq); ok {
						rtt := arrive.UnixNano() - int64(ns)
						stats.periodRTT.add(rtt)
						stats.totalRTT.add(rtt)
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
					fail(fmt.Errorf("payload mismatch at byte %d: got 0x%02x want 0x%02x (possible misaligned chunk)",
						mismatch, rbuf[mismatch], payload[mismatch]))
					return
				}
				stats.verifiedChunks.Add(1)
			}
		}()
	}
}
