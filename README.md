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
- **Configurable Buffers & Duration**: Customize buffer size (`-buf`) and timed execution duration (`-t`).
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
        buffer size in KB (default 1024)
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

Network performance varies across environments and time windows. Under cross-continental transoceanic links (Client in East Asia, Server in North America), benchmark comparisons between protocols:

| Protocol | Throughput |
| :--- | :--- |
| **rhttp** | 0.008478 MB/s |
| **quic** | 0.019454 MB/s |
| **tcp** | 0.563017 MB/s |
| **kcp** | 2.943687 MB/s |
| **rudp** | 3.252355 MB/s |
| **ricmp** | 3.556728 MB/s |

---

## License

This project is licensed under the [MIT License](LICENSE).
