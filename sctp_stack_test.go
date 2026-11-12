//go:build linux && !386
// +build linux,!386

package sctp

import (
	"fmt"
	"net"
	"sync"
	"testing"
)

// atDepth calls f under depth extra stack frames, so that a socket option
// call made by f starts at a different stack offset for each depth and, for
// some depths, grows (and moves) the goroutine's stack on the way to the
// system call.
//
//go:noinline
func atDepth(depth int, f func()) {
	var pad [64]byte
	if depth == 0 {
		f()
		return
	}
	atDepth(depth-1, f)
	pad[0]++ // keeps pad in the frame
}

// TestSockoptAcrossStackGrowth reads socket options from fresh goroutines
// at many stack depths. A socket option value whose address reaches the
// kernel as a stale stack address (the stack moved between the address
// being taken and the system call) shows as a failed or wrong read.
func TestSockoptAcrossStackGrowth(t *testing.T) {
	ln, err := ListenSCTP("sctp", &SCTPAddr{IPAddrs: []net.IPAddr{{IP: net.IPv4(127, 0, 0, 1)}}})
	if err != nil {
		t.Skipf("SCTP unavailable: %v", err)
	}
	defer ln.Close()
	lport := ln.Addr().(*SCTPAddr).Port

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c
	}()
	conn, err := DialSCTP("sctp", nil, &SCTPAddr{IPAddrs: []net.IPAddr{{IP: net.IPv4(127, 0, 0, 1)}}, Port: lport})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if c, ok := <-accepted; ok {
		defer c.Close()
	}
	cport := conn.LocalAddr().(*SCTPAddr).Port
	wantInfo, err := conn.GetDefaultSentParam()
	if err != nil {
		t.Fatal(err)
	}

	const depths, rounds = 400, 10
	var (
		mu    sync.Mutex
		fails []string
		wg    sync.WaitGroup
	)
	fail := func(format string, args ...interface{}) {
		mu.Lock()
		defer mu.Unlock()
		fails = append(fails, fmt.Sprintf(format, args...))
	}
	for r := 0; r < rounds; r++ {
		for d := 0; d < depths; d++ {
			wg.Add(1)
			go func(d int) {
				defer wg.Done()
				atDepth(d, func() {
					if a, ok := ln.Addr().(*SCTPAddr); !ok || a.Port != lport {
						fail("depth %d: listener Addr = %v, want port %d", d, ln.Addr(), lport)
					}
					if a, ok := conn.LocalAddr().(*SCTPAddr); !ok || a.Port != cport {
						fail("depth %d: LocalAddr = %v, want port %d", d, conn.LocalAddr(), cport)
					}
					if a, ok := conn.RemoteAddr().(*SCTPAddr); !ok || a.Port != lport {
						fail("depth %d: RemoteAddr = %v, want port %d", d, conn.RemoteAddr(), lport)
					}
					if info, err := conn.GetDefaultSentParam(); err != nil || *info != *wantInfo {
						fail("depth %d: GetDefaultSentParam = %+v, %v; want %+v", d, info, err, wantInfo)
					}
				})
			}(d)
		}
		wg.Wait()
	}
	if len(fails) > 0 {
		n := len(fails)
		if n > 5 {
			fails = fails[:5]
		}
		t.Fatalf("%d of %d reads failed across stack growth; first: %v", n, depths*rounds*4, fails)
	}
}
