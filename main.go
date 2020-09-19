package main

import (
	"flag"
	"fmt"
	"github.com/esrrhs/go-engine/src/conn"
	"time"
)

var server = flag.String("s", "", "server addr")
var listen = flag.String("l", "", "listen addr")
var proto = flag.String("p", "tcp", "proto "+fmt.Sprintf("%v", conn.SupportReliableProtos()))
var write = flag.Bool("write", false, "write")
var read = flag.Bool("read", false, "read")

func main() {

	flag.Parse()

	if *server == "" && *listen == "" {
		flag.Usage()
		return
	}

	if *write == false && *read == false {
		flag.Usage()
		return
	}

	if !conn.HasReliableProto(*proto) {
		flag.Usage()
		return
	}

	c, err := conn.NewConn(*proto)
	if err != nil {
		fmt.Println(err)
		return
	}

	if *server != "" {
		client, err := c.Dial(*server)
		if err != nil {
			fmt.Println(err)
			return
		}
		show(client)
	}

	if *listen != "" {
		server, err := c.Listen(*listen)
		if err != nil {
			fmt.Println(err)
			return
		}

		for {
			sonny, err := server.Accept()
			if err != nil {
				fmt.Println(err)
				return
			}

			go show(sonny)
		}
	}

}

func show(c conn.Conn) {
	buf := make([]byte, 1024*1024)
	last := time.Now()
	writen := 0
	readn := 0
	for {
		if *write {
			n, err := c.Write(buf)
			if err != nil {
				fmt.Println(err)
				return
			}
			writen += n
		}
		if *read {
			n, err := c.Read(buf)
			if err != nil {
				fmt.Println(err)
				return
			}
			readn += n
		}
		if time.Now().Sub(last) > time.Second {
			elapse := time.Now().Sub(last)
			last = time.Now()

			if *write {
				fmt.Printf("write %f MB/s %v \n", float32(writen)/1024/1024/float32(elapse)*float32(time.Second), c.Info())
			}
			if *read {
				fmt.Printf("read %f MB/s %v \n", float32(readn)/1024/1024/float32(elapse)*float32(time.Second), c.Info())
			}
			writen = 0
			readn = 0
		}
	}
}
