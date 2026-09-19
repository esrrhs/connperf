# connperf

[<img src="https://img.shields.io/github/license/esrrhs/connperf">](https://github.com/esrrhs/connperf)
[<img src="https://img.shields.io/github/languages/top/esrrhs/connperf">](https://github.com/esrrhs/connperf)
[<img src="https://img.shields.io/github/v/release/esrrhs/connperf">](https://github.com/esrrhs/connperf/releases)
[<img src="https://img.shields.io/github/downloads/esrrhs/connperf/total">](https://github.com/esrrhs/connperf/releases)
[<img src="https://img.shields.io/docker/pulls/esrrhs/connperf">](https://hub.docker.com/repository/docker/esrrhs/connperf)
[<img src="https://img.shields.io/github/actions/workflow/status/esrrhs/connperf/go.yml?branch=master">](https://github.com/esrrhs/connperf/actions)

[中文说明](./README_ZH.md)

`connperf` is a high-performance multi-protocol bandwidth and network performance benchmarking tool written in Go. It supports benchmarking across multiple reliable transport protocols: **TCP**, **RUDP**, **RICMP**, **KCP**, **QUIC**, and **RHTTP**.

---

## Features

- **Multi-Protocol Support**: Benchmark across `tcp`, `rudp`, `ricmp`, `kcp`, `quic`, and `rhttp`.
- **Directional & Full-Duplex Modes**: Supports separate `-write` (upload), `-read` (download), or bidirectional simultaneous testing.
- **Fixed Payload Verification**: Sends a deterministic payload (default 1024 bytes) in a loop; the receiver validates every chunk.
- **Configurable Payload & Duration**: Customize payload size (`-buf`, bytes) and timed execution duration (`-t`).
- **Real-Time Throughput & Exit Summary**: Periodic throughput monitoring along with aggregate transfer and average speed summary on exit.
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
        transport protocol: tcp, rudp, ricmp, kcp, quic, rhttp (default "tcp")
  -write
        enable write / sending mode
  -read
        enable read / receiving mode
  -buf int
        fixed payload size in bytes (default 1024); each round is sent/verified as one chunk
  -t int
        test duration in seconds (0 for indefinite until interrupted)
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

### 2. Bidirectional / Full-Duplex Benchmark

* **Server**:
```bash
./connperf -l :8888 -p tcp -read -write
```

* **Client**:
```bash
./connperf -s 127.0.0.1:8888 -p tcp -read -write
```

### 3. Alternative Protocols (e.g. KCP, RUDP, QUIC)

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

Measured on 2026-09-19. The client connected directly to the server over a local 100 Mbps NIC (no VPN), sending one way for about 8 seconds with a fixed 64KB payload. The server verified every chunk. Peaks saturated the local NIC, so the average is a better picture of sustainable throughput on this path.

| Protocol | Peak | Average | Verification |
| :--- | :--- | :--- | :--- |
| **tcp** | 11.31 MB/s (90 Mbps) | 4.91 MB/s | passed |
| **rudp** | 9.69 MB/s (78 Mbps) | 6.16 MB/s | passed |
| **ricmp** | 8.94 MB/s (72 Mbps) | 5.42 MB/s | passed |
| **kcp** | 13.81 MB/s (110 Mbps) | 4.53 MB/s | passed |
| **quic** | 12.50 MB/s (100 Mbps) | about 5.8 MB/s | passed |
| **rhttp** | 1.06 MB/s (8.5 Mbps) | 0.23 MB/s | passed |

---

## License

This project is licensed under the [MIT License](LICENSE).
