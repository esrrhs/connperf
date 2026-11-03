package main

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/esrrhs/gohome/network"
)

// Auto mode port mapping (relative to the base port from -l / -s):
//
//	tcp:   basePort + 0
//	rudp:  basePort + 1
//	kcp:   basePort + 2
//	quic:  basePort + 3
//	rhttp: basePort + 4
//	ricmp: host only (ICMP; no TCP/UDP port). Optional via -auto-ricmp.
//
// Example: -l :8888 → tcp :8888, rudp :8889, kcp :8890, quic :8891, rhttp :8892.
const (
	autoOffsetTCP   = 0
	autoOffsetRUDP  = 1
	autoOffsetKCP   = 2
	autoOffsetQUIC  = 3
	autoOffsetRHTTP = 4
)

// autoProtoSpec describes one candidate protocol in -p auto mode.
type autoProtoSpec struct {
	Proto    string
	Offset   int  // added to basePort; ignored when HostOnly
	HostOnly bool // ricmp: dial/listen host without a port
	Optional bool // listen failure does not abort the auto server (ricmp)
}

// defaultAutoProtos is the ordered candidate list. ricmp is appended only when
// includeRICMP is true.
func defaultAutoProtos(includeRICMP bool) []autoProtoSpec {
	out := []autoProtoSpec{
		{Proto: "tcp", Offset: autoOffsetTCP},
		{Proto: "rudp", Offset: autoOffsetRUDP},
		{Proto: "kcp", Offset: autoOffsetKCP},
		{Proto: "quic", Offset: autoOffsetQUIC},
		{Proto: "rhttp", Offset: autoOffsetRHTTP},
	}
	if includeRICMP {
		out = append(out, autoProtoSpec{Proto: "ricmp", HostOnly: true, Optional: true})
	}
	return out
}

// autoPortOffset returns the documented port offset for a protocol name, or
// false when the protocol is not part of the auto port map (e.g. ricmp).
func autoPortOffset(proto string) (int, bool) {
	switch strings.ToLower(proto) {
	case "tcp":
		return autoOffsetTCP, true
	case "rudp":
		return autoOffsetRUDP, true
	case "kcp":
		return autoOffsetKCP, true
	case "quic":
		return autoOffsetQUIC, true
	case "rhttp":
		return autoOffsetRHTTP, true
	default:
		return 0, false
	}
}

// mapAutoAddr rewrites baseAddr for proto using the auto port scheme.
// baseAddr must be host:port (or :port). For HostOnly protocols, returns host only.
func mapAutoAddr(baseAddr, proto string, hostOnly bool) (string, error) {
	baseAddr = strings.TrimSpace(baseAddr)
	if baseAddr == "" {
		return "", fmt.Errorf("empty address")
	}
	if hostOnly {
		host, _, err := splitAddrHostPort(baseAddr)
		if err != nil {
			// Bare host / IP without port is already fine for ricmp.
			if net.ParseIP(baseAddr) != nil || !strings.Contains(baseAddr, ":") {
				return baseAddr, nil
			}
			return "", err
		}
		if host == "" {
			host = "0.0.0.0"
		}
		return host, nil
	}
	offset, ok := autoPortOffset(proto)
	if !ok {
		return "", fmt.Errorf("no auto port mapping for protocol %q", proto)
	}
	host, port, err := splitAddrHostPort(baseAddr)
	if err != nil {
		return "", err
	}
	mapped := port + offset
	if mapped < 0 || mapped > 65535 {
		return "", fmt.Errorf("mapped port %d for %s out of range (base %d + %d)", mapped, proto, port, offset)
	}
	return net.JoinHostPort(host, strconv.Itoa(mapped)), nil
}

func splitAddrHostPort(addr string) (host string, port int, err error) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("address %q: %w (want host:port)", addr, err)
	}
	port, err = strconv.Atoi(p)
	if err != nil {
		return "", 0, fmt.Errorf("address %q: invalid port %q", addr, p)
	}
	if port < 0 || port > 65535 {
		return "", 0, fmt.Errorf("address %q: port %d out of range", addr, port)
	}
	return h, port, nil
}

// probeResult is one row of the auto-mode comparison table.
type probeResult struct {
	Proto   string
	Addr    string
	AvgBps  float64 // bytes/sec after warmup; 0 if failed
	Bytes   int64
	Elapsed time.Duration
	Err     error
}

// selectProbeWinner returns the index of the highest AvgBps among successful
// probes. Ties prefer the earlier candidate (stable order). Returns -1 if none.
func selectProbeWinner(results []probeResult) int {
	best := -1
	for i, r := range results {
		if r.Err != nil {
			continue
		}
		if best < 0 || r.AvgBps > results[best].AvgBps {
			best = i
		}
	}
	return best
}

func formatProbeTable(results []probeResult, unit string, winner int) string {
	var b strings.Builder
	b.WriteString("auto probe comparison:\n")
	b.WriteString(fmt.Sprintf("%-8s %-22s %12s %10s %s\n", "PROTO", "ADDR", "AVG", "BYTES", "STATUS"))
	for i, r := range results {
		mark := ""
		if i == winner {
			mark = " <-- winner"
		}
		if r.Err != nil {
			b.WriteString(fmt.Sprintf("%-8s %-22s %12s %10s FAIL: %v%s\n",
				r.Proto, r.Addr, "-", "-", r.Err, mark))
			continue
		}
		val, lbl := formatThroughput(r.Bytes, r.Elapsed.Seconds(), unit)
		b.WriteString(fmt.Sprintf("%-8s %-22s %8.2f %-4s %10d OK%s\n",
			r.Proto, r.Addr, val, lbl, r.Bytes, mark))
	}
	return b.String()
}

// probeWriteAvg dials proto/addr and measures average write throughput over
// probeDur after discarding warmup. Uses a single connection and fixed payload.
func probeWriteAvg(parent context.Context, proto, addr string, bufSize int, probeDur, warmup time.Duration) probeResult {
	res := probeResult{Proto: proto, Addr: addr}
	if bufSize <= 0 {
		bufSize = defaultPayloadSize
	}
	if probeDur <= 0 {
		res.Err = fmt.Errorf("probe duration must be > 0")
		return res
	}

	total := probeDur + warmup
	ctx, cancel := context.WithTimeout(parent, total+2*time.Second)
	defer cancel()

	factory, err := network.NewConn(proto)
	if err != nil {
		res.Err = err
		return res
	}
	conn, err := factory.Dial(addr)
	if err != nil {
		res.Err = fmt.Errorf("dial: %w", err)
		return res
	}
	var closeOnce sync.Once
	closeConn := func() {
		closeOnce.Do(func() { _ = conn.Close() })
	}
	defer closeConn()

	go func() {
		<-ctx.Done()
		closeConn()
	}()

	payload := makePayload(bufSize)
	var written atomic.Int64
	start := time.Now()
	statsStart := start.Add(warmup)

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, bufSize)
		copy(buf, payload)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			n, werr := writeFull(conn, buf)
			now := time.Now()
			if n > 0 && !now.Before(statsStart) {
				written.Add(int64(n))
			}
			if werr != nil {
				return
			}
		}
	}()

	timer := time.NewTimer(total)
	defer timer.Stop()
	select {
	case <-timer.C:
		cancel()
	case <-parent.Done():
		cancel()
	case <-ctx.Done():
	}
	closeConn()
	// Some protocols (e.g. quic) may block in Write past Close; do not hang the probe.
	select {
	case <-done:
	case <-time.After(1500 * time.Millisecond):
	}

	elapsed := time.Since(statsStart)
	if elapsed < 0 {
		elapsed = 0
	}
	if elapsed > probeDur+50*time.Millisecond {
		elapsed = probeDur
	}
	res.Bytes = written.Load()
	res.Elapsed = elapsed
	if elapsed <= 0 || res.Bytes == 0 {
		if parent.Err() != nil {
			res.Err = parent.Err()
			return res
		}
		res.Err = fmt.Errorf("no bytes written during probe")
		return res
	}
	res.AvgBps = float64(res.Bytes) / elapsed.Seconds()
	return res
}

func runAutoServer(ctx context.Context, cfg Config) error {
	protos := defaultAutoProtos(cfg.AutoRICMP)
	fmt.Printf("auto server: base listen %s; port map tcp=+0 rudp=+1 kcp=+2 quic=+3 rhttp=+4", cfg.Listen)
	if cfg.AutoRICMP {
		fmt.Printf(" ricmp=host-only")
	} else {
		fmt.Printf(" (ricmp excluded; enable with -auto-ricmp)")
	}
	fmt.Printf("\n")

	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
		mu       sync.Mutex
		started  int
		cancels  []context.CancelFunc
	)
	setErr := func(e error) {
		if e == nil {
			return
		}
		errOnce.Do(func() { firstErr = e })
	}

	listenErr := make(chan error, len(protos))

	for _, spec := range protos {
		addr, err := mapAutoAddr(cfg.Listen, spec.Proto, spec.HostOnly)
		if err != nil {
			return err
		}

		subCtx, subCancel := context.WithCancel(ctx)
		proto := spec.Proto
		listen := addr
		optional := spec.Optional
		child := cfg
		child.Proto = proto
		child.Listen = listen
		child.Server = ""

		mu.Lock()
		cancels = append(cancels, subCancel)
		mu.Unlock()

		wg.Add(1)
		go func() {
			defer wg.Done()
			f, err := network.NewConn(proto)
			if err != nil {
				if optional {
					fmt.Printf("auto: skip %s: %v\n", proto, err)
					listenErr <- nil
					return
				}
				listenErr <- fmt.Errorf("create %s: %w", proto, err)
				setErr(fmt.Errorf("%s: %w", proto, err))
				return
			}
			// Distinguish immediate listen failure for optional protos.
			server, err := f.Listen(listen)
			if err != nil {
				if optional {
					fmt.Printf("auto: skip %s listen %s: %v\n", proto, listen, err)
					listenErr <- nil
					return
				}
				listenErr <- fmt.Errorf("listen %s %s: %w", proto, listen, err)
				setErr(fmt.Errorf("%s: %w", proto, err))
				return
			}
			listenErr <- nil
			mu.Lock()
			started++
			mu.Unlock()

			// Inline accept loop on the already-bound listener. Close exactly
			// once: some Conn implementations (quic) race on concurrent Close.
			var closeOnce sync.Once
			closeServer := func() {
				closeOnce.Do(func() { _ = server.Close() })
			}
			defer closeServer()
			go func() {
				<-subCtx.Done()
				closeServer()
			}()

			maxNote := "unlimited"
			if child.MaxConn > 0 {
				maxNote = fmt.Sprintf("%d", child.MaxConn)
			}
			fmt.Printf("listening on %s (%s), payload %d bytes, maxconn %s...\n",
				listen, proto, child.BufSize, maxNote)

			var (
				awg      sync.WaitGroup
				aErrOnce sync.Once
				aErr     error
				active   atomic.Int64
			)
			setAErr := func(e error) {
				if e == nil {
					return
				}
				aErrOnce.Do(func() { aErr = e })
			}

			for {
				sonny, err := server.Accept()
				if err != nil {
					select {
					case <-subCtx.Done():
						awg.Wait()
						if aErr != nil && ctx.Err() == nil {
							setErr(fmt.Errorf("%s: %w", proto, aErr))
						}
						return
					default:
					}
					if ctx.Err() == nil {
						setErr(fmt.Errorf("%s accept: %w", proto, err))
					}
					return
				}
				if child.MaxConn > 0 && int(active.Load()) >= child.MaxConn {
					_ = sonny.Close()
					continue
				}
				active.Add(1)
				awg.Add(1)
				go func(conn network.Conn) {
					defer awg.Done()
					defer active.Add(-1)
					defer conn.Close()
					connCtx, connCancel := context.WithCancel(subCtx)
					defer connCancel()
					go func() {
						<-connCtx.Done()
						_ = conn.Close()
					}()
					setAErr(showMany(connCtx, []network.Conn{conn}, child))
				}(sonny)
			}
		}()
	}

	// Wait for each listener to report bind success/failure.
	var bindErrs []error
	for range protos {
		select {
		case err := <-listenErr:
			if err != nil {
				bindErrs = append(bindErrs, err)
			}
		case <-time.After(3 * time.Second):
			bindErrs = append(bindErrs, fmt.Errorf("listener startup timed out"))
		case <-ctx.Done():
			mu.Lock()
			for _, c := range cancels {
				c()
			}
			mu.Unlock()
			wg.Wait()
			return ctx.Err()
		}
	}
	if len(bindErrs) > 0 {
		mu.Lock()
		for _, c := range cancels {
			c()
		}
		mu.Unlock()
		wg.Wait()
		return bindErrs[0]
	}

	mu.Lock()
	n := started
	mu.Unlock()
	if n == 0 {
		return fmt.Errorf("auto server: no protocols listening")
	}

	<-ctx.Done()
	mu.Lock()
	for _, c := range cancels {
		c()
	}
	mu.Unlock()
	wg.Wait()
	return firstErr
}

func runAutoClient(ctx context.Context, cfg Config) error {
	protos := defaultAutoProtos(cfg.AutoRICMP)
	probeDur := cfg.ProbeTime
	if probeDur <= 0 {
		probeDur = 2 * time.Second
	}

	fmt.Printf("auto client: probing %d protocol(s) for %s (warmup %s) against base %s\n",
		len(protos), probeDur, cfg.Warmup, cfg.Server)

	results := make([]probeResult, 0, len(protos))
	for _, spec := range protos {
		if ctx.Err() != nil {
			break
		}
		addr, err := mapAutoAddr(cfg.Server, spec.Proto, spec.HostOnly)
		if err != nil {
			results = append(results, probeResult{Proto: spec.Proto, Err: err})
			continue
		}
		fmt.Printf("auto: probing %s %s ...\n", spec.Proto, addr)
		r := probeWriteAvg(ctx, spec.Proto, addr, cfg.BufSize, probeDur, cfg.Warmup)
		r.Proto = spec.Proto
		r.Addr = addr
		results = append(results, r)
	}

	winner := selectProbeWinner(results)
	fmt.Print(formatProbeTable(results, cfg.Unit, winner))

	if winner < 0 {
		return fmt.Errorf("auto: all protocol probes failed")
	}
	if cfg.ProbeOnly {
		fmt.Printf("auto: probe-only; winner would be %s (%s)\n", results[winner].Proto, results[winner].Addr)
		return nil
	}

	w := results[winner]
	fmt.Printf("auto: selected %s → full test on %s\n", w.Proto, w.Addr)

	full := cfg
	full.Proto = w.Proto
	full.Server = w.Addr
	full.Listen = ""
	factory, err := network.NewConn(full.Proto)
	if err != nil {
		return err
	}
	fullCtx := ctx
	if full.Duration > 0 {
		var dCancel context.CancelFunc
		fullCtx, dCancel = context.WithTimeout(ctx, full.Duration)
		defer dCancel()
	}
	return runClient(fullCtx, factory, full)
}

func isAutoProto(p string) bool {
	return strings.EqualFold(strings.TrimSpace(p), "auto")
}
