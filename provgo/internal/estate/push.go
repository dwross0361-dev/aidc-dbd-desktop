package estate

// Configuration push (Python: _push_ready, _pushes, push_override). The push itself stays the bundle's external
// script (push_configs.py, run with python3 in bundle_dir); this engine decides when a device is due, probes it,
// starts the script with the push credential in its environment, and records the result.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"aidc-dbd/provgo/internal/pyjson"
)

// pushProc is one running push (Python: a Popen in self.pushing).
type pushProc struct {
	n    string
	cmd  *exec.Cmd
	log  *os.File
	done atomic.Bool
	rc   int
}

func (p *pushProc) poll() (int, bool) {
	if !p.done.Load() {
		return 0, false
	}
	return p.rc, true
}

// pushState is the engine's push bookkeeping (under e.Mu).
type pushState struct {
	cred    *[2]string
	running []*pushProc // insertion order, as Python's dict
	conc    int
	auto    bool
	once    sync.Once
}

// PushConcurrency is the number of pushes run at once, and whether it was sized by this engine (no push_concurrency
// in estate.conf or the plan's service block) -- DECISIONS P3-1.
func (e *Estate) PushConcurrency() (int, bool) {
	e.push.once.Do(func() {
		if v, ok := e.explicitConf("push_concurrency"); ok {
			e.push.conc = int(pyIntAny(v))
			return
		}
		c := 4 * runtime.NumCPU()
		if c < 8 {
			c = 8
		}
		if c > 64 {
			c = 64
		}
		e.push.conc, e.push.auto = c, true
	})
	return e.push.conc, e.push.auto
}

// explicitConf is a key set in the config file itself or in the plan's service block (not a DEFAULTS value).
func (e *Estate) explicitConf(k string) (any, bool) {
	if b, err := os.ReadFile(e.ConfPath); err == nil {
		if m, err := pyjson.LoadsMap(b); err == nil {
			if v, ok := m.Get(k); ok {
				return v, true
			}
		}
	}
	if svc := e.Plan.M("service"); svc != nil {
		if v, ok := svc.Get(k); ok {
			return v, true
		}
	}
	return nil, false
}

// SetPushCred holds the push credential in memory (console: Run > Push credential).
func (e *Estate) SetPushCred(user, pw string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	e.push.cred = &[2]string{user, pw}
}

func (e *Estate) pushingNames() []any {
	out := []any{}
	for _, p := range e.push.running {
		out = append(out, p.n)
	}
	return out
}

func (e *Estate) isPushing(n string) bool {
	for _, p := range e.push.running {
		if p.n == n {
			return true
		}
	}
	return false
}

func (e *Estate) runningPushes() int {
	k := 0
	for _, p := range e.push.running {
		if _, done := p.poll(); !done {
			k++
		}
	}
	return k
}

// pushReady: the device is due a push (apart from the probe and the concurrency limit). Lock held.
func (e *Estate) pushReady(n string) bool {
	s, d := e.St[n], e.Devs[n]
	if !d.Truthy("push") || s.Truthy("flag") || s.Int("stage") < 2 || s.Int("stage") >= 4 || e.isPushing(n) {
		return false
	}
	if !e.waveOpen(n) {
		return false
	}
	rma := s.M("rma")
	if m := rma.Str("mnha"); m == "await-sync" || m == "done-hold" {
		return false
	}
	if rma.Str("state") == "open" {
		for _, iv := range rma.L("checklist") {
			i, _ := iv.(*pyjson.Map)
			if i.Truthy("blocks_push") && !i.Truthy("done") {
				if s.Str("detail") != "RMA checklist open" {
					s.Set("detail", "RMA checklist open")
					e.saveDev(n)
				}
				return false
			}
		}
	}
	return true
}

var srgPreempt = regexp.MustCompile(`services-redundancy-group \d+ preemption`)

// pushOverride is the configuration file the push uses for this device ("" = the planned file). Lock held.
func (e *Estate) pushOverride(n string) (string, error) {
	r := e.St[n].M("rma")
	if r.Str("state") != "open" {
		return "", nil
	}
	src := ""
	if ap := r.M("apply"); ap.Str("source") == "backup" {
		src = ap.Str("file")
	}
	if r.Str("mnha") == "hold" {
		var base string
		if src != "" {
			t, err := readTextPy(src)
			if err != nil {
				return "", err
			}
			base = t
		} else if t, ok := e.plannedText(n); ok {
			base = t
		}
		var keep []string
		for _, l := range pySplitlines(base) {
			if !srgPreempt.MatchString(l) {
				keep = append(keep, l)
			}
		}
		p := filepath.Join(e.Conf.Str("state_dir"), "rma-hold-"+safeName(n)+".conf")
		if err := os.WriteFile(p, []byte(strings.Join(keep, "\n")+"\n"), 0o644); err != nil {
			return "", err
		}
		return p, nil
	}
	return src, nil
}

func pushPort(d *pyjson.Map) int64 {
	if v, ok := d.M("push").Get("port"); ok {
		return pyIntAny(v)
	}
	return 22
}

// Pushes is one push pass (after each engine tick, as Python's tick() ends with _pushes()).
func (e *Estate) Pushes() {
	conc, _ := e.PushConcurrency()
	type cand struct {
		n, ip string
		port  int64
	}
	var cands []cand
	noCred := func() bool {
		e.Mu.Lock()
		defer e.Mu.Unlock()
		if e.push.cred == nil {
			for _, n := range e.DevOrder {
				s, d := e.St[n], e.Devs[n]
				if d.Truthy("push") && s.Int("stage") >= 2 && s.Int("stage") < 4 && !s.Truthy("flag") && s.Str("detail") != "waiting for the push credential" {
					s.Set("detail", "waiting for the push credential")
					e.saveDev(n)
				}
			}
			return true
		}
		running := e.runningPushes()
		for _, n := range e.DevOrder {
			if running < conc && e.pushReady(n) {
				cands = append(cands, cand{n, e.Devs[n].Str("ip"), pushPort(e.Devs[n])})
			}
		}
		return false
	}()
	if noCred {
		return
	}
	// every candidate is probed at once (Python: a pool of 32, 3 s each): DECISIONS P3-1. Python then starts the first free
	// slots' worth of answering candidates in plan order; that choice is final as soon as every candidate up to the last one
	// chosen has answered or timed out, so the pass does not wait for later probes (a silent device further down the plan
	// no longer holds every push pass for 3 s): DECISIONS P4-11
	e.Mu.Lock()
	free := conc - e.runningPushes()
	e.Mu.Unlock()
	up := probeInOrder(len(cands), free, func(i int) bool {
		if e.gate().active(cands[i].n) { // HS v1.56 r3 (DECISIONS P5-2): a session is open: up, not probed
			return true
		}
		return tcpProbe(cands[i].ip, []int64{cands[i].port}, 3*time.Second)
	})
	e.Mu.Lock()
	defer e.Mu.Unlock()
	if e.push.cred == nil {
		return
	}
	running := e.runningPushes()
	for i, c := range cands {
		n := c.n
		if !up[i] || running >= conc || !e.pushReady(n) {
			continue
		}
		if !e.gate().tryTake(n, e.sshPerDevice()) { // P5-2: the push is a session to the device; at its limit it waits a pass
			continue
		}
		s, d := e.St[n], e.Devs[n]
		args := pyjson.StrList(d.M("push").V("cmd"))
		ov, err := e.pushOverride(n)
		if err != nil {
			e.gate().release(n)
			panic(err) // Python: the OSError ends this push pass (the engine logs it)
		}
		if ov != "" {
			args = append(args, "--config", ov)
		}
		lf, err := os.Create(filepath.Join(e.Conf.Str("state_dir"), "push-"+safeName(n)+".log"))
		if err != nil {
			e.gate().release(n)
			panic(err)
		}
		py, perr := exec.LookPath("python3")
		if perr != nil {
			lf.Close()
			e.gate().release(n)
			e.fail(n, "push could not start: [Errno 2] No such file or directory: 'python3'")
			continue
		}
		cmd := exec.Command(py, args...)
		cmd.Dir = e.Conf.Str("bundle_dir")
		cmd.Env = append(envWithout(os.Environ(), "FABRIC_USER", "FABRIC_PASSWORD"), "FABRIC_USER="+e.push.cred[0], "FABRIC_PASSWORD="+e.push.cred[1])
		cmd.Stdout, cmd.Stderr = lf, lf
		if err := cmd.Start(); err != nil {
			lf.Close()
			e.gate().release(n)
			e.fail(n, "push could not start: "+pyErrno(err))
			continue
		}
		p := &pushProc{n: n, cmd: cmd, log: lf}
		go func() {
			err := cmd.Wait()
			p.rc = exitCode(cmd, err)
			e.gate().release(p.n) // P5-2: the push's session slot, when the push process ends
			p.done.Store(true)
		}()
		e.push.running = append(e.push.running, p)
		running++
		cmd0 := ""
		if len(args) > 0 {
			cmd0 = args[0]
		}
		s.Set("detail", fmt.Sprintf("pushing configuration (%s)", cmd0))
		e.saveDev(n)
		e.ev(e.short(n)+": configuration push started", "info", n)
	}
	var keep []*pushProc
	finished := []*pushProc{}
	for _, p := range e.push.running {
		if _, done := p.poll(); done {
			finished = append(finished, p)
		} else {
			keep = append(keep, p)
		}
	}
	e.push.running = keep
	for _, p := range finished {
		n := p.n
		p.log.Close()
		rma := e.St[n].M("rma")
		switch {
		case p.rc == 0 && rma.Str("state") == "open" && rma.Str("mnha") == "hold":
			rma.Set("mnha", "await-sync")
			e.saveDev(n)
			e.ev(e.short(n)+": pushed with preemption held; waiting for SRG0 session sync", "info", n)
		case p.rc == 0:
			if rma.Str("state") == "open" && rma.Str("mnha") == "restore" {
				rma.Set("mnha", "done")
				for _, iv := range rma.L("checklist") {
					if i, ok := iv.(*pyjson.Map); ok && i.Str("key") == "mnha" {
						i.Set("done", true)
						i.Set("note", "preemption held through sync, then restored by the service")
					}
				}
			}
			e.setStage(n, 4, "configuration pushed and committed", "", "")
		default:
			tail := ""
			if t, err := readTextPy(p.log.Name()); err == nil {
				if ls := pySplitlines(pyStripU(t)); len(ls) > 0 {
					tail = pyHead(ls[len(ls)-1], 160)
				}
			}
			e.fail(n, fmt.Sprintf("push exited %d: %s", p.rc, tail))
		}
	}
}

// probeInOrder runs probe(i) for i in [0, n) at once (at most 4,096 in flight) and returns as soon as the first `want`
// true results in index order are settled: every index before the want-th true one has its result. Results not yet known
// then read false; the probes still in flight finish on their own (their results are dropped).
func probeInOrder(n, want int, probe func(i int) bool) []bool {
	up := make([]bool, n)
	if n == 0 {
		return up
	}
	type res struct {
		i  int
		ok bool
	}
	ch := make(chan res, n) // buffered for every probe: a late one never blocks
	sem := make(chan struct{}, 4096)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for i := 0; i < n; i++ {
			select {
			case sem <- struct{}{}:
			case <-done: // settled: the rest are not started
				for ; i < n; i++ {
					ch <- res{i, false}
				}
				return
			}
			go func(i int) {
				defer func() { <-sem }()
				ch <- res{i, probe(i)}
			}(i)
		}
	}()
	known := make([]bool, n)
	next, found := 0, 0
	for got := 0; got < n; got++ {
		r := <-ch
		known[r.i], up[r.i] = true, r.ok
		for next < n && known[next] && (want <= 0 || found < want) {
			if up[next] {
				found++
			}
			next++
		}
		if want > 0 && found >= want {
			for i := next; i < n; i++ {
				up[i] = false // not settled in order: left for the next pass
			}
			return up
		}
	}
	return up
}

// exitCode is Popen.returncode: the exit status, or minus the signal number.
func exitCode(cmd *exec.Cmd, err error) int {
	if cmd.ProcessState == nil {
		return 1
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -int(ws.Signal())
	}
	return cmd.ProcessState.ExitCode()
}

func envWithout(env []string, keys ...string) []string {
	out := env[:0:0]
outer:
	for _, kv := range env {
		for _, k := range keys {
			if strings.HasPrefix(kv, k+"=") {
				continue outer
			}
		}
		out = append(out, kv)
	}
	return out
}

// parallelCap runs f(i) for i in [0, n) with at most max at once.
func parallelCap(n, max int, f func(i int)) {
	if n == 0 {
		return
	}
	sem := make(chan struct{}, max)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer func() { <-sem; wg.Done() }()
			f(i)
		}(i)
	}
	wg.Wait()
}
