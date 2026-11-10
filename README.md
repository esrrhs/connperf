# connperf

[<img src="https://img.shields.io/github/license/esrrhs/connperf">](https://github.com/esrrhs/connperf)
[<img src="https://img.shields.io/github/languages/top/esrrhs/connperf">](https://github.com/esrrhs/connperf)
[<img src="https://img.shields.io/github/v/release/esrrhs/connperf">](https://github.com/esrrhs/connperf/releases)
[<img src="https://img.shields.io/github/downloads/esrrhs/connperf/total">](https://github.com/esrrhs/connperf/releases)
[<img src="https://img.shields.io/docker/pulls/esrrhs/connperf">](https://hub.docker.com/repository/docker/esrrhs/connperf)
[<img src="https://img.shields.io/github/actions/workflow/status/esrrhs/connperf/go.yml?branch=master">](https://github.com/esrrhs/connperf/actions)

[中文说明](./README_ZH.md)

`connperf` is a high-performance multi-protocol bandwidth and network performance benchmarking tool written in Go. It supports benchmarking across multiple reliable transport protocols: **TCP**, **RUDP**, **RICMP**, **KCP**, **QUIC**, **RHTTP**, and **RDNS**.

---

## Features

- **Multi-Protocol Support**: Benchmark across `tcp`, `rudp`, `ricmp`, `kcp`, `quic`, `rhttp`, and `rdns`.
- **Directional & Full-Duplex Modes**: Supports separate `-write` (upload), `-read` (download), or bidirectional simultaneous testing.
- **Fixed Payload Verification**: Sends a deterministic payload (default 1024 bytes) in a loop; the receiver validates every chunk (header region excluded when RTT headers are in use).
- **Configurable Payload & Duration**: Customize payload size (`-buf`, bytes) and timed execution duration (`-t`).
- **Multi-Connection Client**: `-n` opens N parallel client connections (default 1); periodic lines and the exit summary aggregate throughput across them.
- **Warmup**: `-warmup` seconds of traffic are discarded from stats so averages exclude ramp-up.
- **Server Accept Limit**: `-maxconn` caps concurrent accepted connections on the server (`0` = unlimited).
- **Throughput Units**: Choose `-unit MBps` (default, MiB/s), `Mbps`, or `Gbps` for periodic lines and the exit summary.
- **RTT & Jitter**: In bidirectional (`-read -write`) mode, embeds a 16-byte little-endian header (`seq` + `send_ns`); the peer echoes it on its write path so the sender can report RTT min/avg/max and percentiles p50/p90/p99. Receivers also report RFC3550-ish inter-arrival jitter. One-way tests report throughput + receive jitter only (RTT needs bidirectional; the run does not fail).
- **Real-Time Throughput & Exit Summary**: Periodic throughput monitoring along with aggregate transfer and average speed summary on exit.
- **Auto Protocol Selection**: `-p auto` probes reliable protocols across a documented port map, prints a comparison table, and runs the full test on the winner (`-probe-t`, `-probe-rounds`, `-probe-only`, optional `-auto-ricmp`, `-auto-rdns`).
- **Graceful Shutdown**: Handles OS termination signals (`SIGINT`/`SIGTERM`) cleanly without losing exit statistics.
- **Cross-Platform & Container-Ready**: Prebuilt binaries for Linux, macOS, Windows, and official Docker images.

---

## Installation

### Prebuilt Binaries

Download precompiled archives for Linux, macOS, and Windows from [GitHub Releases](https://github.com/esrrhs/connperf/releases).

### From Source

Ensure Go 1.26+ is installed:

```bash
git clone https://github.com/esrrhs/connperf.git
cd connperf
go build -ldflags="-s -w" -o connperf .
```

### Docker

```bash
docker pull esrrhs/connperf
```

---

## Command Line Options

```text
Usage of connperf:
  -l string
        listen address (server mode, e.g. :8888)
  -s string
        target server address (client mode, e.g. 1.2.3.4:8888)
  -p string
        transport protocol: tcp, rudp, ricmp, kcp, quic, rhttp, rdns, or auto (default "tcp")
  -write
        enable write / sending mode
  -read
        enable read / receiving mode
  -buf int
        fixed payload size in bytes (default 1024); each round is sent/verified as one chunk
  -t int
        test duration in seconds (0 for indefinite until interrupted); in -p auto this applies to the full run after probing
  -unit string
        throughput unit: MBps (default, binary MiB/s), Mbps, or Gbps
  -n int
        client parallel connections (default 1); used for the full auto run (probes use 1 conn)
  -warmup int
        seconds of traffic ignored in stats / discarded from counters (default 0); also applied during auto probes
  -maxconn int
        server max accepted connections (0 = unlimited)
  -probe-t int
        auto mode: per-round sending duration in seconds (1..60, default 2)
  -probe-rounds int
        auto mode: repeats per protocol (1..100, default 3), randomized/interleaved; rank median receiver goodput
  -probe-only
        auto mode: print the probe comparison table and exit without a full run
  -auto-ricmp
        auto mode: include ricmp (needs root/CAP_NET_RAW; excluded by default)
  -auto-rdns
        auto mode: include rdns (requires -rdnsdomain; excluded by default)
  -rdnsdomain string
        RDNS base tunnel domain (e.g. tunnel.example.com; required when using rdns proto)
  -rdnspsk string
        RDNS underlay AES-GCM PSK payload encryption
  -rdnsquerytype string
        RDNS query type: TXT (default) or NULL
  -rdnsupstream string
        RDNS upstream DNS server for non-tunnel queries
  -v
        show concise version
  -version
        show detailed version and build information
```

---

## Usage Examples

### 1. TCP Benchmark

* **Server (Receiving data)**:
```bash
./connperf -l :8888 -p tcp -read
```

* **Client (Sending data)**:
```bash
./connperf -s 127.0.0.1:8888 -p tcp -write
```

Sample output:
```text
write 2065.00 MB/s 127.0.0.1:32758<--tcp-->127.0.0.1:8888
write 1168.00 MB/s 127.0.0.1:32758<--tcp-->127.0.0.1:8888
^C
summary: total write 3233.00 MB, avg speed 1616.50 MB/s [127.0.0.1:32758<--tcp-->127.0.0.1:8888]
```

### 2. Bidirectional / Full-Duplex Benchmark (throughput + RTT + jitter)

* **Server**:
```bash
./connperf -l :8888 -p tcp -read -write
```

* **Client**:
```bash
./connperf -s 127.0.0.1:8888 -p tcp -read -write
```

Sample periodic lines include RTT (sender) and receive jitter when both ends enable `-read` and `-write`:
```text
write 900.00 MB/s rtt min/avg/max/p50/p90/p99 100µs/150µs/400µs/140µs/200µs/350µs 127.0.0.1:32758<--tcp-->127.0.0.1:8888
read 880.00 MB/s jitter 12µs (verified 12345 chunks) 127.0.0.1:32758<--tcp-->127.0.0.1:8888
```

One-way (`-write` only / `-read` only) still reports throughput; the reader also reports jitter and prints a note that RTT needs bidirectional mode.

### 2b. Throughput unit selection

```bash
./connperf -s 127.0.0.1:8888 -p tcp -write -unit Mbps
./connperf -s 127.0.0.1:8888 -p tcp -write -unit Gbps
```

### 2c. Multi-connection, warmup, and server maxconn

```bash
# Server: accept at most 8 concurrent connections
./connperf -l :8888 -p tcp -read -maxconn 8

# Client: 4 parallel connections, ignore first 2s in stats, run 30s
./connperf -s 127.0.0.1:8888 -p tcp -write -n 4 -warmup 2 -t 30
```

Periodic and summary lines report **aggregate** throughput across the `-n` connections (a short note such as `4 conns, e.g. …` identifies the set).

### 2d. Auto protocol selection (`-p auto`)

Server listens on **multiple** reliable protocols using a fixed port map derived from the base port in `-l`. The client performs `-probe-rounds` upload probes per candidate (default **3**), visiting every protocol once in a fresh randomized order each round. Each sends for `-probe-t` seconds (default 2) plus optional `-warmup`. Selection ranks the **median receiver-verified goodput**, never buffered client writes. The winner then runs the ordinary full test (`-t`/`-n`/`-unit`/…) at its mapped port.

Probe details and compatibility:

- Both endpoints must run the new **auto wire v2**. Older auto peers and direct single-protocol clients on auto listeners are intentionally incompatible: missing/invalid reports fail the sample, with **no sender-throughput fallback**. Ordinary single-protocol `-p tcp|rudp|…` wire behavior is unchanged.
- The base **TCP port must be reachable for every candidate**, including UDP/ICMP/DNS candidates. It carries bounded, fixed-size setup/progress/report controls; a random single-use 128-bit token binds each data connection. At most 128 control sessions are registered. With `-maxconn`, allow at least **2** on the TCP listener (control + TCP data); the limit applies per listener, including control connections.
- Server checks each complete frame's sequence and deterministic payload. Goodput = verified payload bytes / server-monotonic elapsed time. The window starts immediately before receiver READY and ends after END is consumed, minus warmup. Frames whose read began before the warmup cutoff are excluded entirely; framing/control bytes are excluded. **Drain time is included**, never clamped to the requested sending duration. This prevents queued writes from inflating bandwidth.
- Probe payload size is `-buf` (**16 bytes..1 MiB**); `-probe-t` is **1..60 seconds**, auto warmup **0..60 seconds**. A progress ACK every at most **1 MiB** bounds outstanding application data on every transport. This is single-stream upload goodput for that bounded window, **not unlimited-window line capacity** (high-BDP links can be window-limited). Full tests keep their existing payload/direction/connection settings and do not use probe framing.
- Table shows median and valid/total rounds; each sample exposes verified bytes, measured duration, receiver bytes/s and validity/error. A strict majority of configured rounds must succeed; median averages the two middle valid samples when even. Exact ties prefer documented protocol order. Failed/zero-byte, malformed, mismatched or missing reports and incomplete cleanup are invalid; if no protocol qualifies, return an error and do not start a full test.
- DNS resolution/dial is bounded by 3 seconds; each session has an overall budget of sending + warmup + 9 seconds (including setup, progress, drain, report and cleanup). Cancellation closes and joins probe IO/watchers. The server reports cleanup complete only **after closing/joining the data connection**, before the next probe. Auto QUIC uses fresh owned sessions with both read/write canceled on close, avoiding gohome's cached sessions/write-only stream Close. Non-auto QUIC is unchanged.
- Probes always measure **upload**, even when the full run is read-only or bidirectional. They require no server `-write` and do not mix benchmark traffic with probing. Remote reports assume a trusted peer; deterministic validation is not authentication. Opt-in RDNS/RICMP still need their environment/privileges and can fail without disqualifying other candidates.

**Port map** (base port `P` from `-l` / `-s`):

| Protocol | Address |
| :--- | :--- |
| **tcp** | `P + 0` |
| **rudp** | `P + 1` |
| **kcp** | `P + 2` |
| **quic** | `P + 3` |
| **rhttp** | `P + 4` |
| **rdns** (opt-in) | `P + 5` (requires `-auto-rdns -rdnsdomain`) |
| **ricmp** (opt-in) | host only (ICMP; no TCP/UDP port) |

`ricmp` and `rdns` are **excluded by default**. Pass `-auto-ricmp` or `-auto-rdns -rdnsdomain <domain>` on both ends to include them.

```bash
# Server: listen on base :8888 → tcp:8888, rudp:8889, kcp:8890, quic:8891, rhttp:8892 (+ rdns:8893 if -auto-rdns)
./connperf -l :8888 -p auto -read

# Client: probe each protocol, print table, then full 30s run on the winner
./connperf -s 127.0.0.1:8888 -p auto -write -probe-t 2 -probe-rounds 3 -t 30

# Probe only (no full run)
./connperf -s 127.0.0.1:8888 -p auto -write -probe-t 2 -probe-rounds 3 -probe-only
```

Open firewall ports `P` through `P+5` (and ICMP if using `-auto-ricmp`). Ensure nothing else binds those derived ports.

### 3. Alternative Protocols (e.g. KCP, RUDP, QUIC, RDNS)

* **KCP**:
```bash
# Server
./connperf -l :8888 -p kcp -read

# Client
./connperf -s 127.0.0.1:8888 -p kcp -write
```

* **RUDP**:
```bash
# Server
./connperf -l :8888 -p rudp -read

# Client
./connperf -s 127.0.0.1:8888 -p rudp -write
```

* **RDNS** (DNS tunnel protocol; requires tunnel base domain):
```bash
# Server (listening on DNS port, e.g. :53 as root)
./connperf -l :53 -p rdns -rdnsdomain tunnel.example.com -read

# Client
./connperf -s 1.2.3.4:53 -p rdns -rdnsdomain tunnel.example.com -write
```

* **RICMP** (requires root/sudo privileges for raw socket handling):
```bash
# Server
sudo ./connperf -l 0.0.0.0:0 -p ricmp -read

# Client
sudo ./connperf -s 1.2.3.4:0 -p ricmp -write
```

### 4. Running via Docker

```bash
# Server
docker run --rm --network host esrrhs/connperf -l :8888 -p tcp -read

# Client
docker run --rm --network host esrrhs/connperf -s 127.0.0.1:8888 -p tcp -write
```

---

## Benchmark Comparison

Measured on 2026-11-07. The benchmark was conducted across a transoceanic public internet link (client located in East Asia, server located in North America) with fixed 1024-byte payloads for 8–10 seconds per protocol. Every transmitted chunk was verified for data integrity by the receiving endpoint. Both downstream (Server -> Client, typical proxy download flow) and upstream (Client -> Server) throughput were evaluated:

| Protocol | Downlink (Server -> Client) Peak / Avg | Uplink (Client -> Server) Peak / Avg | Verification |
| :--- | :--- | :--- | :--- |
| **rudp** | 3.33 MB/s (26.8 Mbps) / 2.25 MB/s (18.0 Mbps) | 10.43 MB/s (83 Mbps) / 4.51 MB/s | passed |
| **ricmp** | 1.03 MB/s (8.3 Mbps) / 0.29 MB/s (2.3 Mbps) | 10.83 MB/s (87 Mbps) / 3.60 MB/s | passed |
| **kcp** | 0.35 MB/s (2.8 Mbps)* / 0.11 MB/s | 19.12 MB/s (153 Mbps) / 15.53 MB/s | passed |
| **tcp** | 0.17 MB/s (1.4 Mbps) / 0.09 MB/s (0.7 Mbps) | 12.64 MB/s (101 Mbps) / 7.81 MB/s | passed |
| **rhttp** | 0.10 MB/s (0.8 Mbps) / 0.05 MB/s (0.4 Mbps) | 1.20 MB/s (9.6 Mbps) / 0.51 MB/s | passed |
| **quic** | <0.01 MB/s (<0.1 Mbps) / 0.00 MB/s | 18.44 MB/s (147 Mbps) / 8.19 MB/s | passed |
| **rdns** | <0.01 MB/s (0.01 Mbps) / 0.00 MB/s | 0.40 MB/s (3.2 Mbps) / 0.05 MB/s | passed |

*\*Note: KCP has no explicit wire handshake in pure one-way server-push (`-read` only on client); the downlink figure reflects bidirectional traffic where the client sends at least one packet to open the session.*

---

## License

This project is licensed under the [MIT License](LICENSE).
