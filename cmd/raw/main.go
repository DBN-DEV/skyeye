package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

const (
	magicWord = "skyeye"
	interval  = time.Second
)

type result struct {
	ip  netip.Addr
	rtt time.Duration
	id  int
	seq int
}

func main() {
	// 从ips.txt文件读取目标IP列表
	ips, err := readIPsFromFile("ips.txt")
	if err != nil {
		log.Fatal(err)
	}

	// 创建ICMP连接
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	// 共享数据结构
	resultCh := make(chan *result)
	stopCh := make(chan struct{})
	var wg sync.WaitGroup

	// 启动三个goroutine
	wg.Add(3)
	go sender(conn, ips, stopCh, &wg)
	go receiver(conn, resultCh, stopCh, &wg)
	go timeoutHandler(resultCh, ips, stopCh, &wg)

	// 设置信号处理
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	// 等待退出信号
	<-sigCh
	close(stopCh)
	wg.Wait()
}

// 发送端实现
func sender(conn *icmp.PacketConn, ips []netip.Addr, stopCh chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	seq := uint16(0)
	magicTs := make([]byte, 14)
	copy(magicTs[0:6], magicWord)

	burstSize := min(100, len(ips))
	burstNum := len(ips) / burstSize
	burstGap := interval / time.Duration(burstNum+1) // +1 是为了保证最后一个周期也能发送

	pingTicker := time.NewTicker(interval)

	for {
		select {
		case <-stopCh:
			return
		case <-pingTicker.C:
			for i := 0; i < burstNum; i++ {
				burstStart := time.Now().UnixNano()
				magicTsUpdate(magicTs, burstStart)
				echo := &icmp.Echo{
					Seq:  int(seq),
					Data: magicTs,
				}
				typ := icmp.Type(ipv4.ICMPTypeEcho)
				b, _ := (&icmp.Message{Type: typ, Code: 0, Body: echo}).Marshal(nil)
				for j := 0; j < burstSize; j++ {
					conn.WriteTo(b, &net.IPAddr{IP: ips[i*100+j].AsSlice()})
				}
				time.Sleep(burstGap)
			}
			seq++
		}
	}
}

// 接收端实现
func receiver(conn *icmp.PacketConn, resultCh chan<- *result, stopCh chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	currentSeq := 0
	buf := make([]byte, 1500)
	conn.SetReadDeadline(time.Now().Add(interval * 2))

	for {
		select {
		case <-stopCh:
			return
		default:
			n, peer, err := conn.ReadFrom(buf)
			// 如果读取超时，则上报本周期结果，重新开始下个周期
			if err != nil && errors.Is(err, os.ErrDeadlineExceeded) {
				resultCh <- nil
				conn.SetReadDeadline(time.Now().Add(interval * 2))
				continue
			}

			peerIp, _ := netip.AddrFromSlice(peer.(*net.IPAddr).IP)

			msg, err := icmp.ParseMessage(1, buf[:n])
			if err != nil {
				continue
			}

			if msg.Type != ipv4.ICMPTypeEchoReply {
				continue
			}
			reply, ok := msg.Body.(*icmp.Echo)
			if !ok {
				continue
			}
			sendTs := magicTsGet(reply.Data)
			if sendTs == 0 {
				continue
			}
			recvTs := time.Now().UnixNano()
			rtt := time.Duration(recvTs - sendTs)

			if reply.Seq > currentSeq {
				// 新周期开始
				currentSeq = reply.Seq
				resultCh <- nil
				conn.SetReadDeadline(time.Now().Add(interval * 2))
			}

			resultCh <- &result{
				ip:  peerIp,
				rtt: rtt,
				id:  reply.ID,
				seq: currentSeq,
			}
		}
	}
}

// 超时处理端
func timeoutHandler(resultCh <-chan *result, ips []netip.Addr, stopCh chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	checkIps := make(map[netip.Addr]struct{}, len(ips))
	for _, ip := range ips {
		checkIps[ip] = struct{}{}
	}

	for {
		select {
		case <-stopCh:
			return
		case r := <-resultCh:
			if r == nil {
				for ip := range checkIps {
					fmt.Printf("ip: %-16s rtt: %-14s\n", ip.String(), "timeout")
				}
				for _, ip := range ips {
					checkIps[ip] = struct{}{}
				}
			} else {
				fmt.Printf("ip: %-16s rtt: %-14s id: %-5d seq: %-5d\n", r.ip.String(), r.rtt.String(), r.id, r.seq)
				delete(checkIps, r.ip)
			}
		}
	}
}

// 从文件读取IP列表
func readIPsFromFile(filename string) ([]netip.Addr, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	ips := make([]netip.Addr, 0, len(lines))

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		ip, err := netip.ParseAddr(line)
		if err != nil {
			continue
		}

		if ip.Is4() {
			ips = append(ips, ip)
		}
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("no valid IPv4 addresses found in file")
	}

	return ips, nil
}

func magicTsUpdate(b []byte, v int64) {
	_ = b[13]
	// BigEndian
	b[6] = byte(v >> 56)
	b[7] = byte(v >> 48)
	b[8] = byte(v >> 40)
	b[9] = byte(v >> 32)
	b[10] = byte(v >> 24)
	b[11] = byte(v >> 16)
	b[12] = byte(v >> 8)
	b[13] = byte(v)
}

func magicTsGet(b []byte) int64 {
	_ = b[13]

	if string(b[0:6]) != magicWord {
		return 0
	}

	return int64(b[13]) | int64(b[12])<<8 | int64(b[11])<<16 | int64(b[10])<<24 |
		int64(b[9])<<32 | int64(b[8])<<40 | int64(b[7])<<48 | int64(b[6])<<56
}
