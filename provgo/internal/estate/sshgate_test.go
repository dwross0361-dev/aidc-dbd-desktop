package estate

import (
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aidc-dbd/provgo/internal/pyjson"
)

type fakeConn struct{ closed atomic.Int32 }

func (f *fakeConn) close() { f.closed.Add(1) }

// The limiter never lets more than `limit` sessions to one device be open at once, whatever the callers.
func TestSSHGateLimit(t *testing.T) {
	for _, lim := range []int{1, 2} {
		g := newSSHGate()
		var cur, peak atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < 24; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				dev := "A"
				if i%3 == 0 {
					dev = "B"
				}
				if g.take(dev, "", lim) != nil {
					t.Error("unexpected reuse outside a pass")
				}
				if dev == "A" {
					if v := cur.Add(1); v > peak.Load() {
						peak.Store(v)
					}
					time.Sleep(5 * time.Millisecond)
					cur.Add(-1)
				}
				g.give(dev, "", &fakeConn{}, true) // not in a pass: closed, slot freed
			}(i)
		}
		wg.Wait()
		if int(peak.Load()) > lim {
			t.Fatalf("limit %d: %d sessions open at once", lim, peak.Load())
		}
		if g.active("A") || g.active("B") {
			t.Fatal("slots left open")
		}
	}
}

// Within a pass a healthy read session is reused by the next read with the same login; the pass closes it at the end.
// A caller with another login evicts it rather than wait.
func TestSSHGateReuse(t *testing.T) {
	g := newSSHGate()
	g.beginPass()
	c := &fakeConn{}
	if g.take("A", "read:ro", 1) != nil {
		t.Fatal("first take must dial")
	}
	g.give("A", "read:ro", c, true)
	if !g.active("A") {
		t.Fatal("an idle session counts as active (probes skip the device)")
	}
	if got := g.take("A", "read:ro", 1); got != c {
		t.Fatal("same login in the pass: the session is reused")
	}
	g.give("A", "read:ro", c, true)
	if g.take("A", "", 1) != nil || c.closed.Load() != 1 {
		t.Fatal("a writer evicts the idle read session (closed) and takes its slot")
	}
	if g.openCount("A") != 1 {
		t.Fatalf("open %d, want 1", g.openCount("A"))
	}
	g.release("A")
	c2 := &fakeConn{}
	g.take("A", "read:ro", 1)
	g.give("A", "read:ro", c2, true)
	g.endPass()
	if c2.closed.Load() != 1 || g.active("A") {
		t.Fatal("endPass closes the idle sessions and frees their slots")
	}
	// outside a pass nothing is kept
	c3 := &fakeConn{}
	g.take("A", "read:ro", 1)
	g.give("A", "read:ro", c3, true)
	if c3.closed.Load() != 1 || g.active("A") {
		t.Fatal("outside a pass the session is closed")
	}
}

// A waiting caller gets the slot as soon as the holder gives it back; tryTake never waits.
func TestSSHGateWaitAndTry(t *testing.T) {
	g := newSSHGate()
	g.take("A", "", 1)
	if g.tryTake("A", 1) {
		t.Fatal("tryTake at the limit must fail")
	}
	got := make(chan struct{})
	go func() { g.take("A", "", 1); close(got) }()
	select {
	case <-got:
		t.Fatal("take must wait at the limit")
	case <-time.After(50 * time.Millisecond):
	}
	g.release("A")
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("waiter not woken")
	}
}

func TestSSHBackoffAndTransient(t *testing.T) {
	want := []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second}
	for k, w := range want {
		if d := sshBackoff(5, k+1); d != w {
			t.Fatalf("backoff %d: %v, want %v", k+1, d, w)
		}
	}
	for _, m := range []string{"\nA paramiko SSHException occurred during connection creation:\n\nError reading SSH protocol banner\n\n",
		"Unable to find prompt: ", "Authentication to device failed.\n...\n\n\nAuthentication timeout.", "Socket is closed",
		"Error reading SSH protocol banner[Errno 104] Connection reset by peer",
		"ssh: handshake failed: read tcp 127.0.0.1:52350->127.50.0.1:22: read: connection reset by peer"} {
		if !isTransientSSH(errors.New(m)) {
			t.Fatalf("transient: %q", m)
		}
	}
	for _, m := range []string{"Authentication to device failed.\n\n\nAuthentication failed.", "TCP connection to device failed.",
		"\nPattern not detected: 'x' in output."} {
		if isTransientSSH(errors.New(m)) {
			t.Fatalf("not transient: %q", m)
		}
	}
	if isTransientSSH(unavailable("Socket is closed")) {
		t.Fatal("ReaderUnavailable is never retried")
	}
}

// sshRetry: bounded attempts, backoff between them, the last error returned; a permanent error stops at once.
func TestSSHRetry(t *testing.T) {
	var slept []time.Duration
	old := sshSleep
	sshSleep = func(d time.Duration) { slept = append(slept, d) }
	defer func() { sshSleep = old }()
	e := &Estate{Conf: pyjson.NewMap()}
	n := 0
	err := e.sshRetry("A", func() error { n++; return errors.New("Error reading SSH protocol banner") })
	if n != 3 || err == nil || len(slept) != 2 || slept[0] != 5*time.Second || slept[1] != 15*time.Second {
		t.Fatalf("default: %d attempts, slept %v", n, slept)
	}
	n, slept = 0, nil
	err = e.sshRetry("A", func() error {
		n++
		if n < 2 {
			return errors.New("Unable to find prompt: ")
		}
		return nil
	})
	if err != nil || n != 2 || len(slept) != 1 {
		t.Fatalf("recovers on the second attempt: %v %d %v", err, n, slept)
	}
	n, slept = 0, nil
	_ = e.sshRetry("A", func() error { n++; return errors.New("Authentication failed.") })
	if n != 1 || len(slept) != 0 {
		t.Fatal("a rejected password is not retried")
	}
	e.Conf.Set("ssh_retries", int64(0))
	n = 0
	_ = e.sshRetry("A", func() error { n++; return errors.New("Socket is closed") })
	if n != 1 {
		t.Fatal("ssh_retries 0: one attempt")
	}
}

func TestBackupStaggerAndOffset(t *testing.T) {
	if staggerDelay(0, 16, 2) != 0 || staggerDelay(3, 16, 2) != 6*time.Second || staggerDelay(16, 16, 2) != 0 || staggerDelay(5, 16, 0) != 0 {
		t.Fatal("stagger: the first workers' jobs i x step apart, later ones not delayed")
	}
	// values written by Python: zlib.crc32(n.encode()) % 1000 / 1000 * min(600, ivl * 0.1)
	cases := map[string]float64{"ACME-ACME-FEF-SP01": 0, "": 0}
	for n := range cases {
		o := backupOffset(n, 6*3600)
		if o < 0 || o >= 600 {
			t.Fatalf("offset %v out of range", o)
		}
	}
	if backupOffset("BEF-SP01", 6*3600) != 282 || backupOffset("x", 1000) != 92.30000000000001 { // Python: crc32 470 and 923
		t.Fatalf("offset differs from Python: %v %v", backupOffset("BEF-SP01", 6*3600), backupOffset("x", 1000))
	}
	if backupOffset("x", 0) != 0 || backupOffset("x", 100) >= 10 {
		t.Fatal("offset bounded by 10 % of the interval")
	}
	seen := map[float64]bool{}
	for _, n := range []string{"BEF-SP01", "BEF-SP02", "FEF-LF01", "FEF-LF02", "FEF-BL01"} {
		seen[backupOffset(n, 6*3600)] = true
	}
	if len(seen) < 4 {
		t.Fatal("offsets spread devices apart")
	}
}

// A server that writes "Exceeded MaxStartups" and closes (OpenSSH past MaxStartups) gives paramiko's banner error, which is
// retried.
func TestDialMaxStartupsDrop(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("Exceeded MaxStartups\r\n"))
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	_, err = dialSession("cumulus", "127.0.0.1", port, "u", "p")
	if err == nil || !strings.Contains(err.Error(), "Error reading SSH protocol banner") || !isTransientSSH(err) {
		t.Fatalf("got %v", err)
	}
}
