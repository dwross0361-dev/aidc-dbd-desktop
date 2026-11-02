package estate

// Management reachability probes (Python: Estate._probes and the prober thread).
//
// Deliberate difference: Python probes with a fixed pool of 32 threads, 3 s per TCP connect, and the three probe groups
// (aggregate members, devices waiting for Verified/reachable, Done devices) one after another, so a site with many
// silent devices stretches one cycle to minutes. Here every probe of a cycle runs at once, on as many goroutines as
// there are probes (capped at ProbeMaxConcurrent), and a device not yet expected up (stages 1-3, waiting to become
// reachable) gets ProbeEarlyTimeout instead of the full ProbeTimeout. A device answering TCP does so in milliseconds on
// the management network, so the results are the ones Python reaches; only the time a cycle takes changes.

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"aidc-dbd/provgo/internal/pyjson"
)

var (
	// ProbeTimeout is the TCP connect timeout for devices expected to answer (Configured, Done).
	ProbeTimeout = 3 * time.Second
	// ProbeEarlyTimeout is the timeout for devices not yet expected up (reachable-signal devices before Config).
	ProbeEarlyTimeout = 1 * time.Second
	// ProbeMaxConcurrent caps the connects in flight in one cycle.
	ProbeMaxConcurrent = 4096
	// ProbePause is the pause between cycles (Python: 5 s).
	ProbePause = 5 * time.Second
)

// ProbeStats describes the last probe cycle (for the scale test and the log).
type ProbeStats struct {
	Probes   int
	Up       int
	Duration time.Duration
}

var lastProbe atomic.Value

// LastProbeStats returns the last cycle's figures.
func LastProbeStats() ProbeStats {
	v, _ := lastProbe.Load().(ProbeStats)
	return v
}

// tcpProbe connects to every port in turn; false at the first that does not answer.
func tcpProbe(ip string, ports []int64, timeout time.Duration) bool {
	for _, p := range ports {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.FormatInt(p, 10)), timeout)
		if err != nil {
			return false
		}
		c.Close()
	}
	return true
}

func portsOf(d *pyjson.Map, key string, def []int64) []int64 {
	v, ok := d.Get(key)
	if !ok {
		return def
	}
	var out []int64
	switch x := v.(type) {
	case []any:
		for _, p := range x {
			out = append(out, pyIntAny(p))
		}
	default:
		out = append(out, pyIntAny(v))
	}
	return out
}

type probeJob struct {
	ip      string
	ports   []int64
	timeout time.Duration
	up      bool
}

func (e *Estate) confFloat(k string, def float64) float64 {
	if v, ok := e.Conf.Get(k); ok {
		return pyFloatAnyZ(v)
	}
	return def
}

// ProbeCycle is one pass of the prober.
func (e *Estate) ProbeCycle() {
	start := time.Now()
	c := e.p2s()
	type todoT struct {
		n     string
		d     *pyjson.Map
		stage int64
		job   int
	}
	type aggT struct {
		n    string
		jobs []int
	}
	type doneT struct {
		n   string
		d   *pyjson.Map
		job int
	}
	var jobs []*probeJob
	var todo []todoT
	var aggs []aggT
	var done []doneT

	e.Mu.Lock()
	t := Now()
	every := e.confFloat("probe_every_s", 15)
	for _, n := range e.DevOrder {
		s, d := e.St[n], e.Devs[n]
		f, stg := s.Str("flag"), s.Int("stage")
		if f == "failed" || f == "quarantined" || stg == 0 || stg == 6 || !d.Truthy("ip") || d.Truthy("aggregate") {
			continue
		}
		if !e.waveOpen(n) {
			continue
		}
		if stg < 4 && d.Str("config_signal") != "reachable" {
			continue
		}
		if t < c.probeDue[n] {
			continue
		}
		c.probeDue[n] = t + every
		to := ProbeTimeout
		if stg < 4 {
			to = ProbeEarlyTimeout
		}
		jobs = append(jobs, &probeJob{ip: d.Str("ip"), ports: portsOf(d, "verify_ports", []int64{22}), timeout: to})
		todo = append(todo, todoT{n, d, stg, len(jobs) - 1})
	}
	for _, n := range e.DevOrder {
		d := e.Devs[n]
		if d.Truthy("probe_ips") && e.waveOpen(n) && e.St[n].Int("stage") < 6 && t >= c.probeDue[n] {
			c.probeDue[n] = t + maxf(60, every)
			a := aggT{n: n}
			port := int64(22)
			if v, ok := d.Get("probe_port"); ok {
				port = pyIntAny(v)
			}
			for _, ip := range pyjson.StrList(d.V("probe_ips")) {
				// the members of an aggregate (DPUs, hosts) come up over the build: short timeout
				jobs = append(jobs, &probeJob{ip: ip, ports: []int64{port}, timeout: ProbeEarlyTimeout})
				a.jobs = append(a.jobs, len(jobs)-1)
			}
			aggs = append(aggs, a)
		}
	}
	doneEvery := e.confFloat("done_probe_s", 120)
	for _, n := range e.DevOrder {
		s, d := e.St[n], e.Devs[n]
		f := s.Str("flag")
		if s.Int("stage") != 6 || (f != "" && f != "down") || !d.Truthy("ip") || d.Truthy("aggregate") || !d.Truthy("verify_ports") {
			continue
		}
		if t < c.probeDue["done:"+n] {
			continue
		}
		c.probeDue["done:"+n] = t + doneEvery
		jobs = append(jobs, &probeJob{ip: d.Str("ip"), ports: portsOf(d, "verify_ports", []int64{22}), timeout: ProbeTimeout})
		done = append(done, doneT{n, d, len(jobs) - 1})
	}
	e.Mu.Unlock()

	// every probe of the cycle at once (bounded)
	conc := len(jobs)
	if conc > ProbeMaxConcurrent {
		conc = ProbeMaxConcurrent
	}
	if conc > 0 {
		sem := make(chan struct{}, conc)
		var wg sync.WaitGroup
		for _, j := range jobs {
			wg.Add(1)
			sem <- struct{}{}
			go func(j *probeJob) {
				defer wg.Done()
				j.up = tcpProbe(j.ip, j.ports, j.timeout)
				<-sem
			}(j)
		}
		wg.Wait()
	}
	up := 0
	for _, j := range jobs {
		if j.up {
			up++
		}
	}

	// results in Python's order: aggregate counts, Done devices, then stage changes
	e.Mu.Lock()
	defer e.Mu.Unlock()
	for _, a := range aggs {
		n := 0
		for _, ji := range a.jobs {
			if jobs[ji].up {
				n++
			}
		}
		cur := e.St[a.n].V("count")
		if !(pyjson.IsNum(cur) && pyjson.Float(cur) == float64(n)) {
			e.checkin(a.n, "count", strconv.Itoa(n), "127.0.0.1")
		}
	}
	for _, x := range done {
		s := e.St[x.n]
		if jobs[x.job].up {
			delete(c.downMiss, x.n)
			if s.Str("flag") == "down" {
				s.Set("flag", "")
				s.Set("detail", "answering again")
				e.saveDev(x.n)
				e.ev(e.short(x.n)+" answers again", "info", x.n)
			}
			continue
		}
		c.downMiss[x.n]++
		if c.downMiss[x.n] >= 2 && s.Str("flag") != "down" {
			s.Set("flag", "down")
			s.Set("detail", fmt.Sprintf("not answering since %s UTC (was Done); if it has failed, Start RMA", time.Now().UTC().Format("15:04")))
			e.saveDev(x.n)
			e.ev(e.short(x.n)+": not answering on its management address (was Done)", "warn", x.n)
		}
	}
	for _, x := range todo {
		if !jobs[x.job].up {
			continue
		}
		mode := "checkin"
		if x.d.Has("config_signal") {
			mode = PyStr(x.d.V("config_signal"))
		}
		if x.stage >= 4 {
			ps := portsOf(x.d, "verify_ports", []int64{22})
			strs := make([]string, len(ps))
			for i, p := range ps {
				strs[i] = strconv.FormatInt(p, 10)
			}
			e.setStage(x.n, 5, "answers on "+joinStr(strs, "/"), "", "")
			e.setStage(x.n, 6, "", "", "")
		} else if mode == "reachable" && x.stage >= 1 {
			how := "DHCP"
			if x.stage >= 2 {
				how = "bootstrap"
			}
			e.setStage(x.n, 4, "configuration inferred: reachable after "+how, "", "")
		}
	}
	ps := ProbeStats{Probes: len(jobs), Up: up, Duration: time.Since(start)}
	lastProbe.Store(ps)
	if os.Getenv("AIDC_PROBE_LOG") != "" || (len(jobs) > 0 && ps.Duration.Seconds() > every) {
		fmt.Fprintf(os.Stderr, "probes: cycle of %d probes (%d up) took %.2fs\n", ps.Probes, ps.Up, ps.Duration.Seconds())
	}
}

func joinStr(l []string, sep string) string {
	out := ""
	for i, s := range l {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// Prober runs probe cycles forever, ProbePause apart.
func (e *Estate) Prober() {
	for {
		func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "probes: %v\n", r)
				}
			}()
			e.ProbeCycle()
		}()
		time.Sleep(ProbePause)
	}
}
