package estate

// SSH to devices: one limiter per device, retries on transient errors, session reuse within a checker pass, and the
// backup stagger (DECISIONS P5-2 .. P5-5; the same rules as the Python service's SSHGate, HS v1.56 r3).
//
// - ssh_per_device (default 1): at most that many SSH sessions to one device at a time, across gate reads, backups,
//   RMA reads, tuning writes, Release DWR access and pushes. A caller waits for a free slot.
// - A device with a session open (in use, or held for reuse) is counted up by the probes without a TCP connect.
// - ssh_retries (default 2), ssh_backoff_s (default 5): a transient failure (banner not read, connection reset, prompt
//   not found, authentication timeout, session closed) is retried after 5 s, then 15 s (base x 3^(n-1)); the last
//   error is the one reported. A refused TCP connection or a rejected password is not retried.
// - Within one checker pass (and one gate re-check) a read session is kept open and reused by the next read of the same
//   device with the same login; the pass closes them when it ends.
// - backup_stagger_s (default 2): the first sessions of a backup pass start that far apart; the next backup of a device
//   is due after backup_every_h plus a per-device offset (crc32 of its name; up to 10 % of the interval, at most 600 s).

import (
	"fmt"
	"hash/crc32"
	"os"
	"strings"
	"sync"
	"time"
)

// sshConn is what the gate holds for reuse (a *cliSession; a fake in tests).
type sshConn interface{ close() }

type idleConn struct {
	key string
	c   sshConn
}

type sshGate struct {
	mu     sync.Mutex
	cond   *sync.Cond
	open   map[string]int // sessions open per device (in use or idle)
	idle   map[string][]idleConn
	passes int
}

func newSSHGate() *sshGate {
	g := &sshGate{open: map[string]int{}, idle: map[string][]idleConn{}}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// take reserves a session slot for the device, waiting while it has limit sessions open. It returns an idle session
// opened earlier with the same key (reuse) or nil (a new slot: the caller dials). An idle session with another key is
// closed to make room.
func (g *sshGate) take(name, key string, limit int) sshConn {
	if limit < 1 {
		limit = 1
	}
	g.mu.Lock()
	for {
		L := g.idle[name]
		if key != "" {
			for i, x := range L {
				if x.key == key {
					g.idle[name] = append(L[:i:i], L[i+1:]...)
					g.mu.Unlock()
					return x.c
				}
			}
		}
		if g.open[name] < limit {
			g.open[name]++
			g.mu.Unlock()
			return nil
		}
		if len(L) > 0 { // full, but one is only held for reuse: close it and take its slot
			x := L[0]
			g.idle[name] = L[1:]
			g.mu.Unlock()
			x.c.close()
			return nil // the slot passes from the closed session to the caller
		}
		g.cond.Wait()
	}
}

// tryTake reserves a slot without waiting (pushes): false when the device is at its limit and nothing idle can go.
func (g *sshGate) tryTake(name string, limit int) bool {
	if limit < 1 {
		limit = 1
	}
	g.mu.Lock()
	if g.open[name] < limit {
		g.open[name]++
		g.mu.Unlock()
		return true
	}
	if L := g.idle[name]; len(L) > 0 {
		x := L[0]
		g.idle[name] = L[1:]
		g.mu.Unlock()
		x.c.close()
		return true
	}
	g.mu.Unlock()
	return false
}

// give returns a slot. keep: the session is healthy and may be reused within the current pass (else it is closed).
func (g *sshGate) give(name, key string, c sshConn, keep bool) {
	g.mu.Lock()
	if keep && c != nil && key != "" && g.passes > 0 {
		g.idle[name] = append(g.idle[name], idleConn{key, c})
		g.cond.Broadcast()
		g.mu.Unlock()
		return
	}
	g.open[name]--
	if g.open[name] <= 0 {
		delete(g.open, name)
	}
	g.cond.Broadcast()
	g.mu.Unlock()
	if c != nil {
		c.close()
	}
}

// release returns a slot that holds no session (a dial that failed, a push that ended).
func (g *sshGate) release(name string) { g.give(name, "", nil, false) }

// active: the device has a session open (the probes count it up without connecting).
func (g *sshGate) active(name string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.open[name] > 0
}

func (g *sshGate) openCount(name string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.open[name]
}

func (g *sshGate) beginPass() {
	g.mu.Lock()
	g.passes++
	g.mu.Unlock()
}

// endPass closes the sessions held for reuse once no pass is running.
func (g *sshGate) endPass() {
	g.mu.Lock()
	g.passes--
	var cl []sshConn
	if g.passes <= 0 {
		g.passes = 0
		for n, L := range g.idle {
			for _, x := range L {
				cl = append(cl, x.c)
				g.open[n]--
				if g.open[n] <= 0 {
					delete(g.open, n)
				}
			}
		}
		g.idle = map[string][]idleConn{}
		g.cond.Broadcast()
	}
	g.mu.Unlock()
	for _, c := range cl {
		c.close()
	}
}

func (e *Estate) gate() *sshGate {
	e.gateOnce.Do(func() { e.sshg = newSSHGate() })
	return e.sshg
}

func (e *Estate) confInt(k string, def int64) int64 {
	if v, ok := e.Conf.Get(k); ok {
		return pyIntAny(v)
	}
	return def
}

func (e *Estate) sshPerDevice() int {
	n := int(e.confInt("ssh_per_device", 1))
	if n < 1 {
		n = 1
	}
	return n
}

func (e *Estate) sshRetries() int {
	n := int(e.confInt("ssh_retries", 2))
	if n < 0 {
		n = 0
	}
	return n
}

// transientSSH: the failures retried (netmiko's and paramiko's wording; the Go reader words them the same).
var transientSSH = []string{"Error reading SSH protocol banner", "Connection reset by peer", "Unable to find prompt",
	"Authentication timeout", "Socket is closed", "SSH session not active"}

func isTransientSSH(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := isUnavailable(err); ok {
		return false
	}
	m := strings.ToLower(err.Error()) // Go words a reset "connection reset by peer", paramiko "Connection reset by peer"
	for _, t := range transientSSH {
		if strings.Contains(m, strings.ToLower(t)) {
			return true
		}
	}
	return false
}

// sshBackoff is the wait before retry number k (1-based): base x 3^(k-1).
func sshBackoff(base float64, k int) time.Duration {
	d := base
	for i := 1; i < k; i++ {
		d *= 3
	}
	return time.Duration(d * float64(time.Second))
}

var sshSleep = time.Sleep

// sshRetry runs f until it succeeds, fails for good, or the retries are spent; it returns the last error.
func (e *Estate) sshRetry(name string, f func() error) error {
	retries, base := e.sshRetries(), e.confFloat("ssh_backoff_s", 5)
	var err error
	for k := 0; ; k++ {
		if err = f(); err == nil || !isTransientSSH(err) || k >= retries {
			return err
		}
		w := sshBackoff(base, k+1)
		fmt.Fprintf(os.Stderr, "ssh: %s: %s; retry %d of %d in %.0fs\n", name, firstLine(err.Error()), k+1, retries, w.Seconds())
		sshSleep(w)
	}
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// dialFor opens a session for a write (no reuse) inside the device's limit, retrying transient failures. The caller
// closes it with done().
func (e *Estate) dialFor(name, os_, host string, port int, user, pw string) (s *cliSession, done func(), err error) {
	g, lim := e.gate(), e.sshPerDevice()
	err = e.sshRetry(name, func() error {
		g.take(name, "", lim)
		x, err := dialSession(os_, host, port, user, pw)
		if err != nil {
			g.release(name)
			return err
		}
		s = x
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return s, func() { g.give(name, "", s, false) }, nil
}

// staggerDelay: job i of a pass of `workers` starts i x step after the first (only the first `workers` jobs wait;
// later ones start as a slot frees).
func staggerDelay(i, workers int, step float64) time.Duration {
	if i <= 0 || i >= workers || step <= 0 {
		return 0
	}
	return time.Duration(float64(i) * step * float64(time.Second))
}

// backupOffset spreads the next scheduled backup of each device over up to 10 % of the interval (at most 600 s).
func backupOffset(n string, ivl float64) float64 {
	span := ivl * 0.1
	if span > 600 {
		span = 600
	}
	if span <= 0 {
		return 0
	}
	return float64(crc32.ChecksumIEEE([]byte(n))%1000) / 1000 * span
}
