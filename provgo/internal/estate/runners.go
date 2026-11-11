package estate

// Runners (Python: runner_ready, runner_worker, rset, maybe_run, _sub, _exec, ssh_argv, _run, _run_dpus, _run_rails,
// _run_accept, _run_isolation). The runners stay external programs (the bundle's scripts, ssh, bash); this engine
// decides when each is due, launches it, captures its output in runner-<key>.log and records the result.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"aidc-dbd/provgo/internal/pyjson"
)

var (
	envKeyRE    = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	subRE       = regexp.MustCompile(`\{([A-Z0-9_]+)\}`)
	shUnsafeRE  = regexp.MustCompile(`[^A-Za-z0-9_@%+=:,./-]`)
	railsOkRE   = regexp.MustCompile(`RAILS ok (\d+)`)
	failLineRE  = regexp.MustCompile(`(?m)^FAIL.*$`)
	ncclRE      = pyRe(`(PASS|FAIL) busbw\s+([0-9.]+)`)
	ncclSetupRE = pyRe(`NCCL setup FAIL[^\n]*`)
)

const dpuModeCheck = "mst start >/dev/null 2>&1; set -- /dev/mst/mt41692_pciconf[0-9]; " +
	"if [ \"$#\" -gt 1 ]; then echo 'several BlueField-3 devices'; exit 43; fi; " +
	"if [ -e \"$1\" ]; then M=$(mlxconfig -d \"$1\" q INTERNAL_CPU_OFFLOAD_ENGINE | grep -o 'INTERNAL_CPU_OFFLOAD_ENGINE.*' | grep -o '([01])' | tr -d '()'); " +
	"if [ \"$M\" = 1 ]; then mlxconfig -y -d \"$1\" s INTERNAL_CPU_OFFLOAD_ENGINE=0 >/dev/null; echo 'NIC mode: set to DPU mode'; exit 42; fi; fi; "

// shQuote is shlex.quote.
func shQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !shUnsafeRE.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func (e *Estate) runnerReady(r *pyjson.Map) []string {
	var miss []string
	sec := e.Secrets()
	for _, k := range pyjson.StrList(r.V("secrets")) {
		if sec[k] == "" {
			miss = append(miss, k)
		}
	}
	for _, k := range pyjson.StrList(r.V("conf")) {
		if !e.Conf.Truthy(k) {
			miss = append(miss, k)
		}
	}
	return miss
}

// RunnerWorker starts due runners (every 10 s; a standby host runs none).
func (e *Estate) RunnerWorker() {
	for {
		if e.roleNow() == "primary" {
			func() {
				defer func() {
					if x := recover(); x != nil {
						fmt.Fprintf(os.Stderr, "runners: %v\n", x)
					}
				}()
				for _, rv := range e.Plan.L("runners") {
					if r, ok := rv.(*pyjson.Map); ok {
						e.maybeRun(r)
					}
				}
			}()
		}
		time.Sleep(10 * time.Second)
	}
}

// rset updates a runner's state (lock held).
func (e *Estate) rset(key string, kv ...any) {
	st, _ := e.RunnerState.V(key).(*pyjson.Map)
	if st == nil {
		st = pyjson.NewMap()
		e.RunnerState.Set(key, st)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		st.Set(kv[i].(string), kv[i+1])
	}
	st.Set("ts", Now())
	e.KVSet("runners", e.RunnerState)
}

// RSet takes the lock and updates a runner's state.
func (e *Estate) RSet(key string, kv ...any) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	e.rset(key, kv...)
}

// RunnerRetry queues a failed runner again (console: Retry).
func (e *Estate) RunnerRetry(key any, user string) (bool, string) {
	ks, isS := key.(string)
	e.Mu.Lock()
	defer e.Mu.Unlock()
	if !isS || !e.RunnerState.Has(ks) {
		return false, "unknown runner"
	}
	e.rset(ks, "state", "queued", "note", "retry by "+user, "retry", true)
	e.RunnerState.M(ks).Set("state", "queued")
	e.audit(user, "runner-retry", ks, "")
	return true, "ok"
}

func (e *Estate) maybeRun(r *pyjson.Map) {
	key := r.Str("key")
	e.Mu.Lock()
	st := e.RunnerState.M(key)
	if s := st.Str("state"); s == "running" || s == "done" || (s == "failed" && !st.Truthy("retry")) {
		e.Mu.Unlock()
		return
	}
	tg := pyjson.StrList(r.V("targets"))
	for _, n := range tg {
		if !e.waveOpen(n) {
			e.Mu.Unlock()
			return
		}
	}
	for _, n := range tg {
		if e.flag(n) == "quarantined" {
			e.Mu.Unlock()
			return
		}
	}
	if need, ok := r.Get("needs_stage"); ok && need != nil {
		for _, n := range tg {
			if !e.Devs[n].Truthy("aggregate") && float64(e.stage(n)) < pyjson.Float(need) {
				e.Mu.Unlock()
				return
			}
		}
	}
	if after := r.Str("after"); r.Truthy("after") && e.RunnerState.M(after).Str("state") != "done" {
		e.Mu.Unlock()
		return
	}
	e.Mu.Unlock()
	miss := e.runnerReady(r)
	e.Mu.Lock()
	defer e.Mu.Unlock()
	if len(miss) > 0 {
		if st.Str("state") != "not configured" {
			e.rset(key, "state", "not configured", "note", "missing "+strings.Join(miss, ", "))
			for _, n := range tg {
				if e.stage(n) < 4 {
					e.St[n].Set("detail", fmt.Sprintf("%s: set %s, or mark configured", PyStr(r.V("label")), strings.Join(miss, ", ")))
					e.saveDev(n)
				}
			}
		}
		return
	}
	e.rset(key, "state", "running", "note", "started", "retry", false)
	var sh []string
	for i, n := range tg {
		if i >= 4 {
			break
		}
		sh = append(sh, e.short(n))
	}
	e.ev(fmt.Sprintf("%s started (%s)", PyStr(r.V("label")), strings.Join(sh, ", ")), "info", "")
	go e.runRunner(r)
}

// sub is _sub: {KEY} from secrets.env, else str() of the configuration value, else "".
func (e *Estate) sub(s string, sec map[string]string) string {
	return subRE.ReplaceAllStringFunc(s, func(m string) string {
		k := m[1 : len(m)-1]
		if v := sec[k]; v != "" {
			return v
		}
		if v, ok := e.Conf.Get(k); ok {
			return PyStr(v)
		}
		return ""
	})
}

func runnerTimeout(r *pyjson.Map) time.Duration {
	t := 7200.0
	if v, ok := r.Get("timeout_s"); ok {
		t = pyjson.Float(v)
	}
	return time.Duration(t * float64(time.Second))
}

// execErr is the OSError text Python's Popen raises.
func execErr(argv0 string, err error) error {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("[Errno 2] No such file or directory: %s", pyStrRepr(argv0))
	}
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("[Errno 13] Permission denied: %s", pyStrRepr(argv0))
	}
	return errors.New(pyErrno(err))
}

// runExec is _exec: argv appended to the log as "$ argv", run in bundle_dir with its output in the log; past the
// runner's timeout_s (7200) it is killed and the result is 124. The whole process group is killed (Python kills the
// direct child only): DECISIONS P3-3.
func (e *Estate) runExec(r *pyjson.Map, argv []string, envAdd [][2]string, logf, stdinPath string) (int, error) {
	log, err := os.OpenFile(logf, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return 0, errors.New(pyErrno(err))
	}
	defer log.Close()
	fmt.Fprintf(log, "$ %s\n", strings.Join(argv, " "))
	var stdin *os.File
	if stdinPath != "" {
		f, err := os.Open(stdinPath)
		if err != nil {
			return 0, errors.New(pyErrno(err))
		}
		defer f.Close()
		stdin = f
	}
	if len(argv) == 0 {
		return 0, errors.New("list index out of range")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if strings.Contains(argv[0], "/") {
		cmd.Path = argv[0]
		cmd.Err = nil
	}
	cmd.Dir = e.Conf.Str("bundle_dir")
	var keys []string
	for _, kv := range envAdd {
		keys = append(keys, kv[0])
	}
	env := envWithout(os.Environ(), keys...)
	for _, kv := range envAdd {
		env = append(env, kv[0]+"="+kv[1])
	}
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = log, log
	if stdin != nil {
		cmd.Stdin = stdin
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return 0, execErr(argv[0], err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return exitCode(cmd, err), nil
	case <-time.After(runnerTimeout(r)):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		return 124, nil
	}
}

func (e *Estate) sshArgv(host, remote string) []string {
	sec := e.Secrets()
	key := sec["ESTATE_SSH_KEY"]
	if key == "" {
		key = "/etc/fabric/estate_ssh_key"
	}
	user := sec["ESTATE_SSH_USER"]
	if user == "" {
		user = "root"
	}
	return []string{"ssh", "-i", key, "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=15",
		user + "@" + host, remote}
}

func readAll(p string) string {
	t, _ := readTextPy(p)
	return t
}

func (e *Estate) runRunner(r *pyjson.Map) {
	sec := e.Secrets()
	key := r.Str("key")
	logf := filepath.Join(e.Conf.Str("state_dir"), "runner-"+key+".log")
	if f, err := os.Create(logf); err == nil {
		f.Close()
	}
	kind := r.Str("kind")
	ok, note := false, ""
	func() {
		defer func() {
			if x := recover(); x != nil {
				ok, note = false, fmt.Sprintf("runner error: %v", x)
			}
		}()
		var err error
		switch kind {
		case "local":
			var argv []string
			for _, a := range pyjson.StrList(r.V("cmd")) {
				argv = append(argv, e.sub(a, sec))
			}
			var env [][2]string
			if em := r.M("env"); em != nil {
				for _, k := range em.Keys() {
					env = append(env, [2]string{k, e.sub(PyStr(em.V(k)), sec)})
				}
			}
			var rc int
			if rc, err = e.runExec(r, argv, env, logf, ""); err == nil {
				out := readAll(logf)
				ok = rc == 0
				if fr := r.Str("fail_regex"); ok && r.Truthy("fail_regex") && pyRe("(?m)"+fr).MatchString(out) {
					ok = false
				}
				note = fmt.Sprintf("exit %d", rc)
			}
		case "ssh-seq":
			ok, note, err = e.runSSHSeq(r, sec, logf)
		case "dpu":
			ok, note, err = e.runDPUs(r, logf)
		case "rails":
			ok, note, err = e.runRails(r, logf)
		case "accept":
			ok, note, err = e.runAccept(r, sec, logf)
		case "isolation":
			ok, note, err = e.runIsolation(r, sec, logf)
		case "inference":
			ok, note, err = e.runInference(r, sec, logf)
		case "failover", "soak", "alerts": // HS v2.04
			ok, note, err = e.runRZ(kind, r, sec, logf)
		case "nccl":
			ok, note, err = e.runNCCL(r, sec, logf)
		}
		if err != nil {
			ok, note = false, "runner error: "+err.Error()
		}
	}()
	e.Mu.Lock()
	defer e.Mu.Unlock()
	st := "failed"
	if ok {
		st = "done"
	}
	e.rset(key, "state", st, "note", note)
	word, lvl := "failed", "fail"
	if ok {
		word, lvl = "passed", "info"
	}
	e.ev(fmt.Sprintf("%s %s: %s", PyStr(r.V("label")), word, note), lvl, "")
	if kind == "local" || kind == "ssh-seq" {
		for _, n := range pyjson.StrList(r.V("targets")) {
			if ok {
				e.setStage(n, 4, fmt.Sprintf("%s: %s", PyStr(r.V("label")), note), "", "")
				if !e.Devs[n].Truthy("ip") {
					e.setStage(n, 6, fmt.Sprintf("%s passed; no management address to probe", PyStr(r.V("label"))), "", "")
				}
			} else {
				e.fail(n, fmt.Sprintf("%s: %s (log: runner-%s.log)", PyStr(r.V("label")), note, key))
			}
		}
	}
}

func (e *Estate) confHosts(r *pyjson.Map) *pyjson.Map {
	h, _ := e.Conf.V(r.Str("hosts_conf")).(*pyjson.Map)
	if h == nil {
		return pyjson.NewMap()
	}
	return h
}

func (e *Estate) runSSHSeq(r *pyjson.Map, sec map[string]string, logf string) (bool, string, error) {
	hosts := e.confHosts(r)
	var bad []string
	keys := pyjson.StrList(r.V("env_keys"))
	for _, k := range keys {
		if !envKeyRE.MatchString(k) {
			bad = append(bad, k)
		}
	}
	if len(bad) > 0 {
		return false, "", fmt.Errorf("invalid environment name(s): %s", strings.Join(bad, ", "))
	}
	var envs []string
	for _, k := range keys {
		var v string
		if cv, ok := e.Conf.Get(k); ok {
			v = PyStr(cv)
		} else {
			v = sec[k]
		}
		envs = append(envs, k+"="+shQuote(v))
	}
	ok, note := true, ""
	for _, sv := range r.L("steps") {
		stp := sv.(*pyjson.Map)
		var on []string
		if stp.Truthy("each") {
			on = hosts.Keys()
		} else {
			on = []string{PyStr(stp.V("on"))}
		}
		for _, h := range on {
			if !hosts.Has(h) {
				ok, note = false, fmt.Sprintf("no address for %s in estate.conf %s", h, r.Str("hosts_conf"))
				break
			}
			rc, err := e.runExec(r, e.sshArgv(PyStr(hosts.V(h)), strings.Join(envs, " ")+" bash -s"), nil, logf,
				pyJoin(e.Conf.Str("bundle_dir"), stp.Str("script")))
			if err != nil {
				return false, "", err
			}
			out := readAll(logf)
			if rc != 0 || (stp.Truthy("fail_regex") && pyRe("(?m)"+stp.Str("fail_regex")).MatchString(out)) {
				ok, note = false, fmt.Sprintf("%s on %s exited %d", stp.Str("script"), h, rc)
				break
			}
		}
		if !ok {
			break
		}
	}
	if note == "" {
		note = "all steps passed"
	}
	return ok, note, nil
}

// orderedPool runs f over n items on w workers; each item is started (start(i) under mu) in input order.
func orderedPool(n, w int, mu *sync.Mutex, start func(i int), f func(i int)) {
	if w < 1 {
		w = 1
	}
	next := 0
	var wg sync.WaitGroup
	for k := 0; k < w && k < n; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if next >= n {
					mu.Unlock()
					return
				}
				i := next
				next++
				start(i)
				mu.Unlock()
				f(i)
			}
		}()
	}
	wg.Wait()
}

func (e *Estate) items(agg string) *pyjson.Map {
	s := e.St[agg]
	it, _ := s.V("items").(*pyjson.Map)
	if it == nil {
		it = pyjson.NewMap()
		s.Set("items", it)
	}
	return it
}

func (e *Estate) runDPUs(r *pyjson.Map, logf string) (bool, string, error) {
	hosts := e.confHosts(r)
	img, _ := filepath.Glob(filepath.Join(e.Conf.Str("web_root"), "images", r.Str("image_glob")))
	sort.Strings(img)
	if len(img) == 0 {
		return false, "no BFB matching images/" + r.Str("image_glob"), nil
	}
	agg := pyjson.StrList(r.V("targets"))[0]
	dpus := r.L("dpus")
	res := make([]bool, len(dpus))
	errs := make([]error, len(dpus))
	par := 8
	if v, ok := e.Conf.Get("dpu_parallel"); ok {
		par = int(pyIntAny(v))
	}
	e.Mu.Lock()
	items := e.items(agg)
	e.Mu.Unlock()
	set := func(h string, st, note string) {
		e.Mu.Lock()
		items.Set(h, pyjson.O("state", st, "note", note))
		e.Mu.Unlock()
	}
	var mu sync.Mutex
	orderedPool(len(dpus), par, &mu, func(i int) {
		x := dpus[i].(*pyjson.Map)
		h := x.Str("host")
		if !hosts.Has(h) {
			set(h, "skipped", "no SSH address")
		} else {
			set(h, "installing", filepath.Base(img[0]))
		}
	}, func(i int) {
		x := dpus[i].(*pyjson.Map)
		h := x.Str("host")
		if !hosts.Has(h) {
			return
		}
		cfg := pyJoin(e.Conf.Str("bundle_dir"), x.Str("cfg"))
		if !isFile(cfg) {
			set(h, "failed", "missing "+x.Str("cfg"))
			return
		}
		rshim := "rshim0"
		if v, ok := x.Get("rshim"); ok {
			rshim = PyStr(v)
		}
		remote := dpuModeCheck + fmt.Sprintf("cat > /tmp/estate.bfb.cfg && curl -fsS -o /tmp/estate.bfb http://%s/images/%s && "+
			"bfb-install --bfb /tmp/estate.bfb --config /tmp/estate.bfb.cfg --rshim %s", e.provIP(), filepath.Base(img[0]), rshim)
		rc, err := e.runExec(r, e.sshArgv(PyStr(hosts.V(h)), remote), nil, logf+"."+safeName(h), cfg)
		if err != nil {
			errs[i] = err
			return
		}
		switch rc {
		case 42:
			set(h, "failed", "was in NIC mode: set to DPU mode -- power-cycle the host, then run again")
		case 43:
			set(h, "failed", "several BlueField-3 devices: confirm the front-end DPU is in DPU mode by hand, then run again")
		default:
			st := "failed"
			if rc == 0 {
				st = "installed"
			}
			set(h, st, fmt.Sprintf("bfb-install exit %d", rc))
			res[i] = rc == 0
		}
	})
	for _, err := range errs {
		if err != nil {
			return false, "", err
		}
	}
	e.Mu.Lock()
	e.saveDev(agg)
	e.Mu.Unlock()
	n := 0
	for _, x := range res {
		if x {
			n++
		}
	}
	return n == len(dpus), fmt.Sprintf("%d of %d DPU images installed (each DPU counts once it answers on its OOB port)", n, len(dpus)), nil
}

func (e *Estate) runRails(r *pyjson.Map, logf string) (bool, string, error) {
	hosts := e.confHosts(r)
	agg := pyjson.StrList(r.V("targets"))[0]
	script := pyJoin(e.Conf.Str("bundle_dir"), r.Str("script"))
	rph := pyIntAny(r.V("rails_per_host"))
	hl := pyjson.StrList(r.V("hosts"))
	got := make([]int64, len(hl))
	type res struct {
		st, note string
	}
	out := make([]*res, len(hl))
	errs := make([]error, len(hl))
	var mu sync.Mutex
	orderedPool(len(hl), 16, &mu, func(int) {}, func(i int) {
		h := hl[i]
		if !hosts.Has(h) {
			out[i] = &res{"skipped", "no SSH address"}
			return
		}
		lf := logf + "." + safeName(h)
		// each attempt starts its host log afresh (P3-c, fixed in both engines): an earlier failure is not read again
		if f, err := os.Create(lf); err == nil {
			f.Close()
		}
		rc, err := e.runExec(r, e.sshArgv(PyStr(hosts.V(h)), fmt.Sprintf("RAILS=%d bash -s", rph)), nil, lf, script)
		if err != nil {
			errs[i] = err
			return
		}
		o := readAll(lf)
		m := railsOkRE.FindStringSubmatch(o)
		var k int64
		note := "no result"
		if m != nil {
			k, _ = strconv.ParseInt(m[1], 10, 64)
			note = m[0]
		}
		if rc != 0 {
			fl := failLineRE.FindAllString(o, -1)
			if len(fl) > 2 {
				fl = fl[:2]
			}
			note += "; " + strings.Join(fl, "; ")
		}
		st := "fail"
		if rc == 0 && k >= rph {
			st = "pass"
		}
		got[i] = k
		if k > rph {
			got[i] = rph // HS v2.05: a host counts its planned rails, no more
		}
		out[i] = &res{st, note}
	})
	for _, err := range errs {
		if err != nil {
			return false, "", err
		}
	}
	var good int64
	for _, k := range got {
		good += k
	}
	e.Mu.Lock()
	defer e.Mu.Unlock()
	items := e.items(agg)
	// Python records each host as it finishes (completion order); here in the plan's host order: DECISIONS P3-4
	for i, h := range hl {
		if out[i] != nil {
			items.Set(h, pyjson.O("state", out[i].st, "note", out[i].note))
		}
	}
	e.saveDev(agg)
	e.checkin(agg, "count", strconv.FormatInt(good, 10), "127.0.0.1")
	aggN := e.Devs[agg].Int("aggregate")
	return good >= aggN, fmt.Sprintf("%d of %d rail links pass", good, aggN), nil
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

func readFrom(p string, pos int64) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := f.Seek(pos, io.SeekStart); err != nil {
		return ""
	}
	b, _ := io.ReadAll(f)
	s := strings.ToValidUTF8(string(b), "�")
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

func (e *Estate) runAccept(r *pyjson.Map, sec map[string]string, logf string) (bool, string, error) {
	cmd := e.Conf.Str("accept_cmd")
	var devs []string
	e.Mu.Lock()
	for _, n := range pyjson.StrList(r.V("devices")) {
		if e.Devs[n] != nil && e.flag(n) != "quarantined" {
			devs = append(devs, n)
		}
	}
	e.Mu.Unlock()
	crit := r.M("criteria")
	if crit == nil {
		crit = pyjson.NewMap()
	}
	tests := []string{"T1", "T2", "T3", "T4"}
	if r.Has("tests") {
		tests = pyjson.StrList(r.V("tests"))
	}
	pod := "1"
	if r.Truthy("pod") {
		pod = PyStr(r.V("pod"))
	}
	out := pyjson.NewMap()
	for _, t := range tests {
		var s0, s1 *AccSnap
		if len(devs) > 0 {
			s0 = e.accSnap(devs)
		}
		pos := fileSize(logf)
		c := strings.ReplaceAll(strings.ReplaceAll(cmd, "{TEST}", t), "{POD}", pod)
		rc, err := e.runExec(r, []string{"bash", "-c", e.sub(c, sec)}, [][2]string{{"ACCEPT_TEST", t}, {"ACCEPT_POD", pod}}, logf, "")
		if err != nil {
			return false, "", err
		}
		if len(devs) > 0 {
			s1 = e.accSnap(devs)
		}
		txt := readFrom(logf, pos)
		res := e.accScore(t, txt, s0, s1, crit, rc)
		out.Set(t, res)
		lvl := "warn"
		if res.Str("result") == "pass" {
			lvl = "info"
		}
		e.Event(fmt.Sprintf("Lossless acceptance %s: %s", t, res.Str("result")), lvl)
	}
	e.SubmitAcceptResult(out, "runner")
	var bad []string
	all := true
	for _, t := range tests {
		switch out.M(t).Str("result") {
		case "blocked":
			bad = append(bad, t)
			all = false
		case "pass":
		default:
			all = false
		}
	}
	switch {
	case all:
		return len(bad) == 0, "all tests pass", nil
	case len(bad) > 0:
		return false, "failed: " + strings.Join(bad, ", "), nil
	}
	return true, "some criteria need confirming by hand", nil
}

func (e *Estate) runIsolation(r *pyjson.Map, sec map[string]string, logf string) (bool, string, error) {
	rc, err := e.runExec(r, []string{"bash", "-c", e.sub(e.Conf.Str("isolation_cmd"), sec)}, nil, logf, "")
	if err != nil {
		return false, "", err
	}
	txt := readAll(logf)
	ok, msg := e.SubmitIsolationText(txt, "runner")
	if !ok {
		return false, fmt.Sprintf("exit %d; %s", rc, msg), nil
	}
	e.Mu.Lock()
	res := e.isolationEval()
	lvl := "warn"
	if res.Str("result") == "pass" {
		lvl = "info"
	}
	e.ev("Ingress isolation: "+res.Str("note"), lvl, "")
	e.Mu.Unlock()
	return res.Str("result") == "pass", res.Str("note"), nil
}

func (e *Estate) runNCCL(r *pyjson.Map, sec map[string]string, logf string) (bool, string, error) {
	rc, err := e.runExec(r, []string{"bash", "-c", e.sub(e.Conf.Str("nccl_cmd"), sec)}, nil, logf, "")
	if err != nil {
		return false, "", err
	}
	out := readAll(logf)
	m := ncclRE.FindStringSubmatch(out)
	if m != nil {
		var tgt any = int64(0)
		if e.Conf.Truthy("TARGET_BUSBW") {
			tgt = e.Conf.V("TARGET_BUSBW")
		} else if sec["TARGET_BUSBW"] != "" {
			tgt = sec["TARGET_BUSBW"]
		}
		if _, ok := pyFloat(m[2]); !ok {
			return false, "", fmt.Errorf("could not convert string to float: %s", pyStrRepr(m[2]))
		}
		if _, ok := pyFloatAny(tgt); !ok {
			return false, "", fmt.Errorf("could not convert string to float: %s", pyStrRepr(PyStr(tgt)))
		}
		e.SubmitResult("nccl", m[2], tgt, "runner", pyTail(out, 1500))
		return m[1] == "PASS", m[0], nil
	}
	if m2 := ncclSetupRE.FindString(out); m2 != "" { // HS v2.05: nothing measured, and why
		return false, m2, nil
	}
	return false, fmt.Sprintf("no PASS/FAIL line (exit %d)", rc), nil
}
