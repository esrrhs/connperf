# connperf

[<img src="https://img.shields.io/github/license/esrrhs/connperf">](https://github.com/esrrhs/connperf)
[<img src="https://img.shields.io/github/languages/top/esrrhs/connperf">](https://github.com/esrrhs/connperf)
[<img src="https://img.shields.io/github/v/release/esrrhs/connperf">](https://github.com/esrrhs/connperf/releases)
[<img src="https://img.shields.io/github/downloads/esrrhs/connperf/total">](https://github.com/esrrhs/connperf/releases)
[<img src="https://img.shields.io/docker/pulls/esrrhs/connperf">](https://hub.docker.com/repository/docker/esrrhs/connperf)
[<img src="https://img.shields.io/github/actions/workflow/status/esrrhs/connperf/go.yml?branch=master">](https://github.com/esrrhs/connperf/actions)

[English](./README.md)

`connperf` 是一款基于 Go 语言编写的高性能多协议带宽与网络性能基准测试工具。支持对多种可靠传输协议进行吞吐量测试：**TCP**、**RUDP**、**RICMP**、**KCP**、**QUIC**、**RHTTP**、**RDNS**。

---

## 功能特性

- **多协议支持**：支持 `tcp`、`rudp`、`ricmp`、`kcp`、`quic`、`rhttp`、`rdns` 多种可靠传输协议。
- **单向与全双工模式**：支持单独 `-write`（发送端测速）、单独 `-read`（接收端测速）或同时双向收发测试。
- **固定内容校验**：循环发送确定性 payload（默认 1024 字节），接收端逐块校验内容是否一致（启用 RTT 头时校验跳过头部区域）。
- **灵活的配置选项**：支持自定义 payload 大小（`-buf`，单位字节）与定时测试时长（`-t`）。
- **多连接客户端**：`-n` 并行打开 N 条客户端连接（默认 1）；周期输出与退出汇总为跨连接聚合吞吐。
- **预热**：`-warmup` 秒内的流量不计入统计，平均速率排除爬升阶段。
- **服务端连接上限**：`-maxconn` 限制服务端同时接受的连接数（`0` 表示不限制）。
- **吞吐单位**：通过 `-unit MBps`（默认，二进制 MiB/s）、`Mbps` 或 `Gbps` 选择周期输出与汇总的单位。
- **RTT 与抖动**：双向（`-read -write`）模式下在 payload 前嵌入 16 字节小端头（`seq` + `send_ns`），对端在写路径上回显该头，发送端据此统计 RTT min/avg/max 以及百分位 p50/p90/p99；接收端同时输出近似 RFC3550 的到达间隔抖动。单向测试仅报告吞吐与接收抖动（RTT 需要双向，不会因此失败）。
- **实时吞吐监控与汇总**：每秒输出实时速率，退出时打印总传输数据量与全程平均带宽。
- **自动协议选择**：`-p auto` 按文档化端口映射探测各可靠协议，打印对比表，并在胜出协议上跑完整测试（`-probe-t`、`-probe-only`，可选 `-auto-ricmp`、`-auto-rdns`）。
- **优雅退出处理**：捕获系统信号（`SIGINT`/`SIGTERM`），退出时不丢失统计数据并释放资源。
- **跨平台与容器化支持**：提供 Linux、macOS、Windows 预编译二进制文件及 Docker 容器镜像。

---

## 安装

### 下载预编译二进制文件

请前往 [GitHub Releases](https://github.com/esrrhs/connperf/releases) 页面下载适合您系统的最新发布包。

### 源码编译

要求本地环境已安装 Go 1.26 及以上版本：

```bash
git clone https://github.com/esrrhs/connperf.git
cd connperf
go build -ldflags="-s -w" -o connperf .
```

### Docker 镜像

```bash
docker pull esrrhs/connperf
```

---

## 命令行参数

```text
Usage of connperf:
  -l string
        监听地址（服务端模式，如 :8888）
  -s string
        目标服务器地址（客户端模式，如 1.2.3.4:8888）
  -p string
        传输协议类型：tcp, rudp, ricmp, kcp, quic, rhttp, rdns，或 auto（默认 "tcp"）
  -write
        开启发送/写入模式
  -read
        开启接收/读取模式
  -buf int
        固定 payload 大小，单位字节（默认 1024）；每轮按该长度发送并校验
  -t int
        测试持续时间，单位秒（默认 0，表示持续运行直至手动中断）；`-p auto` 时仅作用于探测之后的完整测试
  -unit string
        吞吐单位：MBps（默认，二进制 MiB/s）、Mbps 或 Gbps
  -n int
        客户端并行连接数（默认 1）；用于 auto 的完整测试（探测阶段固定 1 条连接）
  -warmup int
        统计忽略的预热秒数 / 丢弃计数器（默认 0）；auto 探测阶段同样生效
  -maxconn int
        服务端最大接受连接数（0 表示不限制）
  -probe-t int
        auto 模式：每个协议的探测时长（秒，默认 2）
  -probe-only
        auto 模式：只打印探测对比表后退出，不跑完整测试
  -auto-ricmp
        auto 模式：纳入 ricmp（需要 root/CAP_NET_RAW；默认排除）
  -auto-rdns
        auto 模式：纳入 rdns（需要配置 -rdnsdomain；默认排除）
  -rdnsdomain string
        RDNS 基础隧道域名（例如 tunnel.example.com；使用 rdns 协议时必须配置）
  -rdnspsk string
        RDNS 底层 AES-GCM 载荷加密密钥（可选）
  -rdnsquerytype string
        RDNS DNS 查询类型：TXT（默认）或 NULL
  -rdnsupstream string
        RDNS 上游 DNS 服务器（保留供递归解析）
  -v
        显示简短版本号
  -version
        显示详细版本与构建元数据
```

---

## 使用示例

### 1. TCP 带宽测试

* **服务端（接收端）**：
```bash
./connperf -l :8888 -p tcp -read
```

* **客户端（发送端）**：
```bash
./connperf -s 127.0.0.1:8888 -p tcp -write
```

控制台输出样例：
```text
write 2065.00 MB/s 127.0.0.1:32758<--tcp-->127.0.0.1:8888
write 1168.00 MB/s 127.0.0.1:32758<--tcp-->127.0.0.1:8888
^C
summary: total write 3233.00 MB, avg speed 1616.50 MB/s [127.0.0.1:32758<--tcp-->127.0.0.1:8888]
```

### 2. 双向 / 全双工并发测试（吞吐 + RTT + 抖动）

* **服务端**：
```bash
./connperf -l :8888 -p tcp -read -write
```

* **客户端**：
```bash
./connperf -s 127.0.0.1:8888 -p tcp -read -write
```

两端都开启 `-read` 与 `-write` 时，周期输出会包含发送端 RTT 与接收端抖动，例如：
```text
write 900.00 MB/s rtt min/avg/max/p50/p90/p99 100µs/150µs/400µs/140µs/200µs/350µs 127.0.0.1:32758<--tcp-->127.0.0.1:8888
read 880.00 MB/s jitter 12µs (verified 12345 chunks) 127.0.0.1:32758<--tcp-->127.0.0.1:8888
```

单向（仅 `-write` / 仅 `-read`）仍报告吞吐；接收端还会报告抖动，并提示 RTT 需要双向模式。

### 2b. 选择吞吐单位

```bash
./connperf -s 127.0.0.1:8888 -p tcp -write -unit Mbps
./connperf -s 127.0.0.1:8888 -p tcp -write -unit Gbps
```

### 2c. 多连接、预热与服务端 maxconn

```bash
# 服务端：最多同时接受 8 条连接
./connperf -l :8888 -p tcp -read -maxconn 8

# 客户端：4 条并行连接，前 2 秒不计入统计，跑 30 秒
./connperf -s 127.0.0.1:8888 -p tcp -write -n 4 -warmup 2 -t 30
```

周期输出与汇总为跨 `-n` 连接的**聚合**吞吐（会带类似 `4 conns, e.g. …` 的简短说明）。

### 2d. 自动协议选择（`-p auto`）

服务端按 `-l` 中的**基准端口**同时监听多种可靠协议（固定端口映射）。客户端对每个候选协议探测 `-probe-t` 秒（默认 2），在可选 `-warmup` 之后统计**写**吞吐平均值，打印对比表，再在胜出协议对应的映射端口上执行完整测试（`-t`/`-n`/`-unit`/…）。

**端口映射**（基准端口为 `-l` / `-s` 中的 `P`）：

| 协议 | 地址 |
| :--- | :--- |
| **tcp** | `P + 0` |
| **rudp** | `P + 1` |
| **kcp** | `P + 2` |
| **quic** | `P + 3` |
| **rhttp** | `P + 4` |
| **rdns**（可选） | `P + 5`（需配合 `-auto-rdns -rdnsdomain`） |
| **ricmp**（可选） | 仅主机（ICMP；无 TCP/UDP 端口） |

`ricmp` 与 `rdns` **默认排除**。两端都加 `-auto-ricmp` 或 `-auto-rdns -rdnsdomain <domain>` 才会纳入。

```bash
# 服务端：基准 :8888 → tcp:8888, rudp:8889, kcp:8890, quic:8891, rhttp:8892 (加 -auto-rdns 则多 rdns:8893)
./connperf -l :8888 -p auto -read

# 客户端：探测各协议并打印表格，再对胜出者跑 30 秒完整测试
./connperf -s 127.0.0.1:8888 -p auto -write -probe-t 2 -t 30

# 仅探测（不跑完整测试）
./connperf -s 127.0.0.1:8888 -p auto -write -probe-t 2 -probe-only
```

请放行防火墙端口 `P` 到 `P+5`（若使用 `-auto-ricmp` 还需放行 ICMP），并确保派生端口未被占用。

### 3. 其他协议（KCP、RUDP、QUIC、RDNS 等）

* **KCP**：
```bash
# 服务端
./connperf -l :8888 -p kcp -read

# 客户端
./connperf -s 127.0.0.1:8888 -p kcp -write
```

* **RUDP**：
```bash
# 服务端
./connperf -l :8888 -p rudp -read

# 客户端
./connperf -s 127.0.0.1:8888 -p rudp -write
```

* **RDNS**（DNS 隧道协议，需指定基础域名）：
```bash
# 服务端（监听 DNS 端口，如需 :53 可由 root 启动）
./connperf -l :53 -p rdns -rdnsdomain tunnel.example.com -read

# 客户端
./connperf -s 1.2.3.4:53 -p rdns -rdnsdomain tunnel.example.com -write
```

* **RICMP**（需 root/管理员权限操作原始套接字）：
```bash
# 服务端
sudo ./connperf -l 0.0.0.0:0 -p ricmp -read

# 客户端
sudo ./connperf -s 1.2.3.4:0 -p ricmp -write
```

### 4. Docker 运行

```bash
# 服务端
docker run --rm --network host esrrhs/connperf -l :8888 -p tcp -read

# 客户端
docker run --rm --network host esrrhs/connperf -s 127.0.0.1:8888 -p tcp -write
```

---

## 协议速度对比

2026-11-07，在跨洲跨洋公网链路（客户端位于东亚，服务端位于北美）环境下测试，固定 payload 1024 字节，每项协议测试 8–10 秒，接收端逐块校验数据内容完整性。测试分别评估了下行（服务端 -> 客户端，代表典型代理下载流量）与上行（客户端 -> 服务端）的吞吐表现：

| 协议 | 下行（Server -> Client）峰值 / 平均 | 上行（Client -> Server）峰值 / 平均 | 校验 |
| :--- | :--- | :--- | :--- |
| **rudp** | 3.33 MB/s（26.8 Mbps） / 2.25 MB/s（18.0 Mbps） | 10.43 MB/s（83 Mbps） / 4.51 MB/s | 通过 |
| **ricmp** | 1.03 MB/s（8.3 Mbps） / 0.29 MB/s（2.3 Mbps） | 10.83 MB/s（87 Mbps） / 3.60 MB/s | 通过 |
| **kcp** | 0.35 MB/s（2.8 Mbps）* / 0.11 MB/s | 19.12 MB/s（153 Mbps） / 15.53 MB/s | 通过 |
| **tcp** | 0.17 MB/s（1.4 Mbps） / 0.09 MB/s（0.7 Mbps） | 12.64 MB/s（101 Mbps） / 7.81 MB/s | 通过 |
| **rhttp** | 0.10 MB/s（0.8 Mbps） / 0.05 MB/s（0.4 Mbps） | 1.20 MB/s（9.6 Mbps） / 0.51 MB/s | 通过 |
| **quic** | <0.01 MB/s（<0.1 Mbps） / 0.00 MB/s | 18.44 MB/s（147 Mbps） / 8.19 MB/s | 通过 |
| **rdns** | <0.01 MB/s（0.01 Mbps） / 0.00 MB/s | 0.40 MB/s（3.2 Mbps） / 0.05 MB/s | 通过 |

*\*注：KCP 在纯单向服务端推送（客户端仅 `-read`）模式下无底层握手包触发服务端会话；表中下行数据为客户端发包触发会话后的实测表现。*

---

## 开源协议

本项目采用 [MIT License](LICENSE) 许可协议。
