# connperf

[<img src="https://img.shields.io/github/license/esrrhs/connperf">](https://github.com/esrrhs/connperf)
[<img src="https://img.shields.io/github/languages/top/esrrhs/connperf">](https://github.com/esrrhs/connperf)
[![Go Report Card](https://goreportcard.com/badge/github.com/esrrhs/connperf)](https://goreportcard.com/report/github.com/esrrhs/connperf)
[<img src="https://img.shields.io/github/v/release/esrrhs/connperf">](https://github.com/esrrhs/connperf/releases)
[<img src="https://img.shields.io/github/downloads/esrrhs/connperf/total">](https://github.com/esrrhs/connperf/releases)
[<img src="https://img.shields.io/docker/pulls/esrrhs/connperf">](https://hub.docker.com/repository/docker/esrrhs/connperf)
[<img src="https://img.shields.io/github/actions/workflow/status/esrrhs/connperf/go.yml?branch=master">](https://github.com/esrrhs/connperf/actions)

[English](./README.md)

`connperf` 是一款基于 Go 语言编写的高性能多协议带宽与网络性能基准测试工具。支持对多种可靠传输协议进行吞吐量测试：**TCP**、**RUDP**、**RICMP**、**KCP**、**QUIC**、**RHTTP**。

---

## 功能特性

- **多协议支持**：支持 `tcp`、`rudp`、`ricmp`、`kcp`、`quic`、`rhttp` 多种可靠传输协议。
- **单向与全双工模式**：支持单独 `-write`（发送端测速）、单独 `-read`（接收端测速）或同时双向收发测试。
- **灵活的配置选项**：支持自定义数据缓冲区大小（`-buf`）与定时测试时长（`-t`）。
- **实时吞吐监控与汇总**：每秒输出实时速率，退出时打印总传输数据量与全程平均带宽。
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
        传输协议类型：tcp, rudp, ricmp, kcp, quic, rhttp（默认 "tcp"）
  -write
        开启发送/写入模式
  -read
        开启接收/读取模式
  -buf int
        缓冲区大小，单位 KB（默认 1024）
  -t int
        测试持续时间，单位秒（默认 0，表示持续运行直至手动中断）
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

### 2. 双向 / 全双工并发测试

* **服务端**：
```bash
./connperf -l :8888 -p tcp -read -write
```

* **客户端**：
```bash
./connperf -s 127.0.0.1:8888 -p tcp -read -write
```

### 3. 其他协议（KCP、RUDP、QUIC 等）

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

不同网络环境及不同时段下传输速率可能差异明显。以下为真实远距离跨洋链路（客户端位于大陆，服务端位于北美）实测数据对比：

| 代理协议 | 吞吐速度 |
| :--- | :--- |
| **rhttp** | 0.008478 MB/s |
| **quic** | 0.019454 MB/s |
| **tcp** | 0.563017 MB/s |
| **kcp** | 2.943687 MB/s |
| **rudp** | 3.252355 MB/s |
| **ricmp** | 3.556728 MB/s |

---

## 开源协议

本项目采用 [MIT License](LICENSE) 许可协议。
