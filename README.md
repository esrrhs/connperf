# connperf
多协议带宽测试工具，支持协议：tcp、rudp、ricmp、kcp、quic

# 示例
* tcp server
```
# ./connperf -l :8888 -p tcp -read
read 2057 MB/s 127.0.0.1:8888<--tcp-->127.0.0.1:32758 
read 1169 MB/s 127.0.0.1:8888<--tcp-->127.0.0.1:32758 
read 1178 MB/s 127.0.0.1:8888<--tcp-->127.0.0.1:32758 
read 1633 MB/s 127.0.0.1:8888<--tcp-->127.0.0.1:32758 
```
* tcp client
```
# ./connperf -s 127.0.0.1:8888 -p tcp -write
write 2065 MB/s 127.0.0.1:32758<--tcp-->127.0.0.1:8888 
write 1168 MB/s 127.0.0.1:32758<--tcp-->127.0.0.1:8888 
write 1179 MB/s 127.0.0.1:32758<--tcp-->127.0.0.1:8888 
write 1629 MB/s 127.0.0.1:32758<--tcp-->127.0.0.1:8888 
```
* 使用docker
```
# docker run --rm --network host esrrhs/connperf ./connperf -l :8888 -p tcp -read
# docker run --rm --network host esrrhs/connperf ./connperf -s 127.0.0.1:8888 -p tcp -write
```

# 协议对比
* 不同网络环境，不同时间段，速度变化很大。client位于大陆，server位于北美，速度如下：

|     代理方式   | 速度  |
|--------------|----------|
| quic | 0.019454 MB/s |
| tcp | 0.563017 MB/s |
| kcp | 2.943687 MB/s |
| rudp | 3.252355 MB/s |
| ricmp | 3.556728 MB/s |

