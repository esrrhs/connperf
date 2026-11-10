package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"sort"
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
//	rdns:  basePort + 5 (optional via -auto-rdns, requires -rdnsdomain)
//	ricmp: host only (ICMP; no TCP/UDP port). Optional via -auto-ricmp.
//
// Example: -l :8888 → tcp :8888, rudp :8889, kcp :8890, quic :8891, rhttp :8892, rdns :8893.
const (
	autoOffsetTCP   = 0
	autoOffsetRUDP  = 1
	autoOffsetKCP   = 2
	autoOffsetQUIC  = 3
	autoOffsetRHTTP = 4
	autoOffsetRDNS  = 5
)

// autoProtoSpec describes one candidate protocol in -p auto mode.
type autoProtoSpec struct {
	Proto    string
	Offset   int  // added to basePort; ignored when HostOnly
	HostOnly bool // ricmp: dial/listen host without a port
	Optional bool // listen failure does not abort the auto server (ricmp)
}

// defaultAutoProtos is the ordered candidate list. ricmp is appended only when
// includeRICMP is true; rdns is appended only when includeRDNS is true.
func defaultAutoProtos(includeRICMP, includeRDNS bool) []autoProtoSpec {
	out := []autoProtoSpec{
		{Proto: "tcp", Offset: autoOffsetTCP},
		{Proto: "rudp", Offset: autoOffsetRUDP},
		{Proto: "kcp", Offset: autoOffsetKCP},
		{Proto: "quic", Offset: autoOffsetQUIC},
		{Proto: "rhttp", Offset: autoOffsetRHTTP},
	}
	if includeRDNS {
		out = append(out, autoProtoSpec{Proto: "rdns", Offset: autoOffsetRDNS, Optional: true})
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
	case "rdns":
		return autoOffsetRDNS, true
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
	Samples []probeResult
	Err     error
}

// selectProbeWinner returns the index of the highest AvgBps among successful
// probes. Ties prefer the earlier candidate (stable order). Returns -1 if none.
func selectProbeWinner(results []probeResult) int {
	best := -1
	for i, r := range results {
		if r.Err != nil || r.AvgBps <= 0 {
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
	b.WriteString(fmt.Sprintf("%-8s %-22s %12s %10s %s\n", "PROTO", "ADDR", "MEDIAN", "VALID", "STATUS"))
	for i, r := range results {
		valid := 0
		for _, sample := range r.Samples {
			if sample.Err == nil && sample.AvgBps > 0 {
				valid++
			}
		}
		validity := fmt.Sprintf("%d/%d", valid, len(r.Samples))
		mark := ""
		if i == winner {
			mark = " <-- winner"
		}
		if r.Err != nil {
			b.WriteString(fmt.Sprintf("%-8s %-22s %12s %10s FAIL: %v%s\n",
				r.Proto, r.Addr, "-", validity, r.Err, mark))
			for j, sample := range r.Samples {
				fmt.Fprintf(&b, "  round %d: bytes=%d elapsed=%s receiver_Bps=%.2f valid=%t error=%v\n", j+1, sample.Bytes, sample.Elapsed, sample.AvgBps, sample.Err == nil && sample.AvgBps > 0, sample.Err)
			}
			continue
		}
		val, lbl := formatThroughput(1000000, 1000000/r.AvgBps, unit)
		b.WriteString(fmt.Sprintf("%-8s %-22s %8.2f %-4s %10s OK%s\n",
			r.Proto, r.Addr, val, lbl, validity, mark))
		for j, sample := range r.Samples {
			fmt.Fprintf(&b, "  round %d: bytes=%d elapsed=%s receiver_Bps=%.2f valid=%t error=%v\n", j+1, sample.Bytes, sample.Elapsed, sample.AvgBps, sample.Err == nil && sample.AvgBps > 0, sample.Err)
		}
	}
	return b.String()
}

func runAutoServer(ctx context.Context, cfg Config) error {
	cfg.autoHub = &probeHub{sessions: make(map[[16]byte]*probeSession)}
	protos := defaultAutoProtos(cfg.AutoRICMP, cfg.AutoRDNS)
	fmt.Printf("auto server: base listen %s; port map tcp=+0 rudp=+1 kcp=+2 quic=+3 rhttp=+4", cfg.Listen)
	if cfg.AutoRDNS {
		fmt.Printf(" rdns=+%d", autoOffsetRDNS)
	}
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
			f, err := newAutoConn(proto, child)
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
			listenerDone := make(chan struct{})
			go func() {
				defer close(listenerDone)
				<-subCtx.Done()
				closeServer()
			}()
			defer func() { subCancel(); closeServer(); <-listenerDone }()

			maxNote := "unlimited"
			if child.MaxConn > 0 {
				maxNote = fmt.Sprintf("%d", child.MaxConn)
			}
			fmt.Printf("listening on %s (%s), payload %d bytes, maxconn %s...\n",
				listen, proto, child.BufSize, maxNote)

			var (
				awg    sync.WaitGroup
				active atomic.Int64
			)

			defer func() { subCancel(); awg.Wait() }()
			for {
				sonny, err := server.Accept()
				if err != nil {
					select {
					case <-subCtx.Done():
						awg.Wait()
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
					// Probe failures are local to a sample, not fatal to the listener.
					if err := serveAutoConn(subCtx, conn, child); err != nil && subCtx.Err() == nil {
						fmt.Printf("auto %s connection: %v\n", proto, err)
					}
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
	protos := defaultAutoProtos(cfg.AutoRICMP, cfg.AutoRDNS)
	probeDur := cfg.ProbeTime
	if probeDur <= 0 {
		probeDur = 2 * time.Second
	}

	size := cfg.BufSize
	if size <= 0 {
		size = defaultPayloadSize
	}
	if err := validateProbe(size, probeDur, cfg.Warmup); err != nil {
		return err
	}

	rounds := cfg.ProbeRounds
	if rounds == 0 {
		rounds = 3
	}
	if rounds < 1 || rounds > 100 {
		return fmt.Errorf("-probe-rounds must be 1..100")
	}
	fmt.Printf("auto client: %d rounds x %d protocols, randomized/interleaved; median receiver goodput, duration %s warmup %s base %s\n", rounds, len(protos), probeDur, cfg.Warmup, cfg.Server)
	results := make([]probeResult, len(protos))
	for i, spec := range protos {
		addr, err := mapAutoAddr(cfg.Server, spec.Proto, spec.HostOnly)
		results[i] = probeResult{Proto: spec.Proto, Addr: addr, Err: err}
	}
	schedule := probeSchedule(len(protos), rounds, rand.Shuffle)
	for _, i := range schedule {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r := &results[i]
		sample := probeResult{Proto: r.Proto, Addr: r.Addr, Err: r.Err}
		if r.Err == nil {
			fmt.Printf("auto: probing %s round %d/%d %s ...\n", r.Proto, len(r.Samples)+1, rounds, r.Addr)
			sample = probeReceiverGoodput(ctx, r.Proto, r.Addr, cfg.BufSize, probeDur, cfg.Warmup, cfg)
		}
		r.Samples = append(r.Samples, sample)
	}
	for i := range results {
		summarizeProbe(&results[i], rounds)
	}
	if ctx.Err() != nil {
		return ctx.Err()
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
	full.autoTransport = true
	full.Proto = w.Proto
	full.Server = w.Addr
	full.Listen = ""
	factory, err := newAutoConn(full.Proto, full)
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

// One randomized permutation per round interleaves protocols instead of
// completing all repeats of a protocol together. Ties retain candidate order.
func probeSchedule(n, rounds int, shuffle func(int, func(int, int))) []int {
	out := make([]int, 0, n*rounds)
	for r := 0; r < rounds; r++ {
		order := make([]int, n)
		for i := range order {
			order[i] = i
		}
		shuffle(n, func(i, j int) { order[i], order[j] = order[j], order[i] })
		out = append(out, order...)
	}
	return out
}

func summarizeProbe(r *probeResult, rounds int) {
	var valid []probeResult
	for _, s := range r.Samples {
		if s.Err == nil && s.AvgBps > 0 {
			valid = append(valid, s)
		}
	}
	// Require a strict majority; a lone lucky sample cannot beat reliable peers.
	if len(valid) < rounds/2+1 {
		r.Err = fmt.Errorf("only %d/%d valid receiver reports (majority required)", len(valid), rounds)
		return
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].AvgBps < valid[j].AvgBps })
	mid := len(valid) / 2
	r.AvgBps = valid[mid].AvgBps
	if len(valid)%2 == 0 {
		r.AvgBps = (valid[mid-1].AvgBps + r.AvgBps) / 2
	}
	r.Bytes, r.Elapsed, r.Err = valid[mid].Bytes, valid[mid].Elapsed, nil
}
