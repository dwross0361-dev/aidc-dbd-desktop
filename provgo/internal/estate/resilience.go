package estate

// Resilience (builder HS-v2.04; Python: _rz_*, submit_failover_text, failover_eval, submit_soak_text, soak_eval,
// submit_alerts_text, alerts_eval, _run_rz, gate_kind_eval). Failover with traffic running (acceptance/failover.sh), the soak
// (acceptance/soak.sh) and the alert path (acceptance/alerts.sh): the scripts report measurements, scored here against the
// plan's "resilience" targets, the same in both engines.

import (
	"fmt"
	"sort"
	"strings"

	"aidc-dbd/provgo/internal/pyjson"
)

func rzKV(s string) *pyjson.Map {
	out := pyjson.NewMap()
	for _, p := range accKVRE.FindAllStringSubmatch(s, -1) {
		if f, ok := pyFloat(p[2]); ok {
			out.Set(p[1], f)
		} else {
			out.Set(p[1], pyHead(p[2], 200))
		}
	}
	return out
}

// rzf is _rzf: a reported number, or 0 for anything else.
func rzf(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

func (e *Estate) rzStore(kind string, rec *pyjson.Map, user, summary string) (bool, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	rec.Set("by", user)
	rec.Set("ts", Now())
	e.Results.Set(kind, rec)
	e.KVSet("results", e.Results)
	e.audit(user, "result-"+kind, "", summary)
	e.autoPopKind(kind)
	return true, "recorded"
}

func (e *Estate) rzPlan(kind string) *pyjson.Map {
	if r := e.Plan.M("resilience"); r != nil {
		if m := r.M(kind); m != nil {
			return m
		}
	}
	return pyjson.NewMap()
}

type rzLine struct {
	ok   any // true, false or nil (to confirm)
	note string
}

type rzEv struct {
	keys []string
	m    map[string]rzLine
}

func (v *rzEv) set(k string, ok any, note string) {
	if v.m == nil {
		v.m = map[string]rzLine{}
	}
	if _, have := v.m[k]; !have {
		v.keys = append(v.keys, k)
	}
	v.m[k] = rzLine{ok, note}
}

func rzVerdict(v *rzEv, by, what string) *pyjson.Map {
	var bad, unk []string
	evo := pyjson.NewMap()
	for _, k := range v.keys {
		x := v.m[k]
		if x.ok == nil {
			unk = append(unk, k)
			evo.Set(k, pyjson.O("ok", false, "note", "confirm: "+x.note))
		} else {
			if x.ok == false {
				bad = append(bad, k)
			}
			evo.Set(k, pyjson.O("ok", x.ok == true, "note", x.note))
		}
	}
	if len(bad) > 0 {
		sh := bad
		if len(sh) > 4 {
			sh = sh[:4]
		}
		more := ""
		if len(bad) > 4 {
			more = fmt.Sprintf(" +%d more", len(bad)-4)
		}
		return pyjson.O("result", "blocked", "note", fmt.Sprintf("%d failing: %s (%s)", len(bad), strings.Join(sh, ", ")+more, by), "evidence", evo)
	}
	if len(unk) > 0 {
		sh := unk
		if len(sh) > 4 {
			sh = sh[:4]
		}
		return pyjson.O("result", "needs confirmation", "note", fmt.Sprintf("%d to confirm: %s (%s)", len(unk), strings.Join(sh, ", "), by), "evidence", evo)
	}
	return pyjson.O("result", "pass", "note", fmt.Sprintf("%s (%s)", what, by), "evidence", evo)
}

func caseLabel(c *pyjson.Map) string { return PyStr(dget(c, "label", c.V("id"))) }

func intDef(v any, def int64) int64 {
	if !pyjson.Truthy(v) {
		return def
	}
	return int64(pyFloatAnyZ(v))
}

// ---- failover

var foRE = pyRe(`(?m)^FAILOVER\s+(\S+)\s+(.*)$`)

// SubmitFailoverText records acceptance/failover.sh's output.
func (e *Estate) SubmitFailoverText(text, user string) (bool, string) {
	C := pyjson.NewMap()
	for _, m := range foRE.FindAllStringSubmatch(text, -1) {
		C.Set(m[1], rzKV(m[2]))
	}
	if C.Len() == 0 {
		return false, "no FAILOVER lines in the text (paste the output of acceptance/failover.sh)"
	}
	return e.rzStore("failover", pyjson.O("cases", C), user, fmt.Sprintf("%d case(s)", C.Len()))
}

func (e *Estate) failoverEval(planes []string) *pyjson.Map {
	var cases []*pyjson.Map
	for _, cv := range e.rzPlan("failover").L("cases") {
		c := cv.(*pyjson.Map)
		if len(planes) == 0 || containsStr(planes, PyStr(c.V("plane"))) {
			cases = append(cases, c)
		}
	}
	r := e.Results.M("failover")
	if !pyjson.Truthy(r) {
		return pyjson.O("result", "waiting", "note", "waiting for the failover run (runner or uploaded results)", "evidence", pyjson.NewMap())
	}
	got := r.M("cases")
	if got == nil {
		got = pyjson.NewMap()
	}
	ev := &rzEv{}
	for _, c := range cases {
		id := PyStr(c.V("id"))
		tgt := intDef(c.V("target_ms"), 1000)
		g, _ := got.V(id).(*pyjson.Map)
		switch {
		case g == nil:
			ev.set(id, nil, caseLabel(c)+": not run")
		case g.Has("error"):
			ev.set(id, false, caseLabel(c)+": "+PyStr(g.V("error")))
		default:
			dn, up, n, rs := rzf(g.V("down_ms")), rzf(g.V("up_ms")), rzf(g.V("streams")), rzf(g.V("restored"))
			ok := n > 0 && rs == 1 && dn <= float64(tgt) && up <= float64(tgt)
			tail := ""
			if rs != 1 {
				tail = ", NOT restored"
			}
			ev.set(id, ok, fmt.Sprintf("%s: down %s ms, back %s ms (target %d ms), %s streams%s", caseLabel(c), pyPctF(dn, 0), pyPctF(up, 0), tgt, pyPctD(n), tail))
		}
	}
	if len(cases) == 0 {
		return pyjson.O("result", "pass", "note", "no failover cases for this plane", "evidence", pyjson.NewMap())
	}
	return rzVerdict(ev, PyStr(dget(r, "by", "")), fmt.Sprintf("%d failover case(s) within target", len(cases)))
}

// ---- soak

var (
	soakItRE   = pyRe(`(?m)^SOAK\s+iter=(\d+)\s+busbw=([0-9]+(?:\.[0-9]+)?)`)
	soakHostRE = pyRe(`(?m)^SOAK\s+host\s+(\S+)\s+(-?\d+)`)
	soakDoneRE = pyRe(`(?m)^SOAK\s+done\s+hours=([0-9]+(?:\.[0-9]+)?)\s+iters=(\d+)`)
)

// SubmitSoakText records acceptance/soak.sh's output; sw is the switches' drop and watchdog counters over the run (runner only).
func (e *Estate) SubmitSoakText(text, user string, sw *pyjson.Map) (bool, string) {
	it := []any{}
	for _, m := range soakItRE.FindAllStringSubmatch(text, -1) {
		f, _ := pyFloat(m[2])
		it = append(it, f)
	}
	host := pyjson.NewMap()
	for _, m := range soakHostRE.FindAllStringSubmatch(text, -1) {
		f, _ := pyFloat(m[2])
		host.Set(m[1], rzf(host.V(m[1]))+f)
	}
	d := soakDoneRE.FindAllStringSubmatch(text, -1)
	if len(it) == 0 && len(d) == 0 {
		return false, "no SOAK lines in the text (paste the output of acceptance/soak.sh)"
	}
	if len(it) > 20000 {
		it = it[len(it)-20000:]
	}
	hours := 0.0
	if len(d) > 0 {
		hours, _ = pyFloat(d[len(d)-1][1])
	}
	var swv any
	if sw != nil {
		swv = sw
	}
	rec := pyjson.O("iters", it, "host", host, "hours", hours, "done", len(d) > 0, "switch", swv)
	return e.rzStore("soak", rec, user, fmt.Sprintf("%d run(s), %s h", len(it), pyPctF(hours, 1)))
}

func (e *Estate) soakEval() *pyjson.Map {
	P := e.rzPlan("soak")
	r := e.Results.M("soak")
	if !pyjson.Truthy(r) {
		return pyjson.O("result", "waiting", "note", "waiting for the soak (runner or uploaded results)", "evidence", pyjson.NewMap())
	}
	H, D := 24.0, 5.0
	if v := P.V("hours"); pyjson.Truthy(v) {
		H = pyFloatAnyZ(v)
	}
	if v := P.V("drift_pct"); pyjson.Truthy(v) {
		D = pyFloatAnyZ(v)
	}
	var it []float64
	for _, x := range r.L("iters") {
		it = append(it, pyFloatAnyZ(x))
	}
	host := r.M("host")
	hours := 0.0
	if v := r.V("hours"); pyjson.Truthy(v) {
		hours = pyFloatAnyZ(v)
	}
	ev := &rzEv{}
	tail := ""
	if !r.Truthy("done") {
		tail = " (no end line)"
	}
	ev.set("soak ran its full duration", r.Truthy("done") && hours >= H, fmt.Sprintf("%s of %s hours%s", pyPctF(hours, 1), pyPctD(H), tail))
	if len(it) > 0 {
		s := append([]float64(nil), it...)
		sort.Float64s(s)
		n := len(s)
		med := s[n/2]
		if n%2 == 0 {
			med = (s[n/2-1] + s[n/2]) / 2.0
		}
		ev.set("bus bandwidth steady through the soak", s[0] >= (100.0-D)/100.0*med,
			fmt.Sprintf("lowest %s GB/s against a median of %s (within %s%%), %d runs", pyPctF(s[0], 1), pyPctF(med, 1), pyPctD(D), n))
		tgt := 0.0
		if v := e.Conf.V("TARGET_BUSBW"); pyjson.Truthy(v) {
			tgt = pyFloatAnyZ(v)
		} else if sv := e.Secrets()["TARGET_BUSBW"]; sv != "" {
			tgt = pyFloatAnyZ(sv)
		}
		if tgt != 0 {
			ev.set("bus bandwidth at target", s[0] >= tgt, fmt.Sprintf("lowest %s GB/s against %s", pyPctF(s[0], 1), pyPctF(tgt, 1)))
		} else {
			ev.set("bus bandwidth at target", nil, fmt.Sprintf("TARGET_BUSBW not set (lowest %s GB/s)", pyPctF(s[0], 1)))
		}
	} else {
		ev.set("bus bandwidth steady through the soak", false, "no NCCL runs reported")
	}
	if host != nil && host.Len() > 0 {
		g := func(ks ...string) float64 {
			t := 0.0
			for _, k := range ks {
				t += pyFloatAnyZ(dget(host, k, 0.0))
			}
			return t
		}
		ev.set("no link flaps on the host links", g("link_down_events_phy") == 0, fmt.Sprintf("%s link-down events", pyPctD(g("link_down_events_phy"))))
		ev.set("no uncorrectable errors on the host links", g("rx_symbol_err_phy") == 0, fmt.Sprintf("%s symbol errors", pyPctD(g("rx_symbol_err_phy"))))
		l := g("rx_discards_phy", "out_of_sequence", "packet_seq_err")
		ev.set("no loss at the hosts", l == 0, fmt.Sprintf("%s discards and sequence errors", pyPctD(l)))
	} else {
		ev.set("host counters", nil, "no SOAK host lines: confirm the host links by hand")
	}
	if sw := r.M("switch"); pyjson.Truthy(sw) {
		seen := map[string]bool{}
		for _, x := range pyjson.StrList(sw.V("seen")) {
			seen[x] = true
		}
		dr, wd := pyFloatAnyZ(dget(sw, "drops", 0.0)), pyFloatAnyZ(dget(sw, "wd", 0.0))
		if seen["drops"] {
			ev.set("no lossless drops at the switches", dr == 0, fmt.Sprintf("%s dropped", pyPctD(dr)))
		} else {
			ev.set("no lossless drops at the switches", nil, "drop counters not understood: confirm by hand")
		}
		if seen["wd"] {
			ev.set("no PFC watchdog events", wd == 0, fmt.Sprintf("%s events", pyPctD(wd)))
		} else {
			ev.set("no PFC watchdog events", nil, "watchdog counters not understood: confirm by hand")
		}
	} else {
		ev.set("switch counters", nil, "uploaded by hand: confirm no lossless drops and no watchdog events from the switches")
	}
	return rzVerdict(ev, PyStr(dget(r, "by", "")), fmt.Sprintf("%s-hour soak clean", pyPctF(hours, 0)))
}

// ---- alerts

var (
	alRE = pyRe(`(?m)^ALERT\s+(\S+)\s+(.*)$`)
	tmRE = pyRe(`(?m)^TELEMETRY\s+(.*)$`)
)

// SubmitAlertsText records acceptance/alerts.sh's output.
func (e *Estate) SubmitAlertsText(text, user string) (bool, string) {
	C := pyjson.NewMap()
	for _, m := range alRE.FindAllStringSubmatch(text, -1) {
		C.Set(m[1], rzKV(m[2]))
	}
	t := tmRE.FindAllStringSubmatch(text, -1)
	if C.Len() == 0 && len(t) == 0 {
		return false, "no ALERT or TELEMETRY lines in the text (paste the output of acceptance/alerts.sh)"
	}
	var tv any
	if len(t) > 0 {
		tv = rzKV(t[len(t)-1][1])
	}
	return e.rzStore("alerts", pyjson.O("cases", C, "telemetry", tv), user, fmt.Sprintf("%d alert case(s)", C.Len()))
}

func (e *Estate) alertsEval() *pyjson.Map {
	P := e.rzPlan("alerts")
	r := e.Results.M("alerts")
	if !pyjson.Truthy(r) {
		return pyjson.O("result", "waiting", "note", "waiting for the alert test (runner or uploaded results)", "evidence", pyjson.NewMap())
	}
	got := r.M("cases")
	if got == nil {
		got = pyjson.NewMap()
	}
	ev := &rzEv{}
	for _, cv := range P.L("cases") {
		c := cv.(*pyjson.Map)
		id := PyStr(c.V("id"))
		tgt := intDef(c.V("target_s"), 120)
		g, _ := got.V(id).(*pyjson.Map)
		switch {
		case g == nil:
			ev.set(id, nil, caseLabel(c)+": not run")
		case g.Has("error"):
			ev.set(id, false, caseLabel(c)+": "+PyStr(g.V("error")))
		default:
			det, dl, cl := rzf(g.V("detected")), rzf(g.V("delay_s")), rzf(g.V("cleared"))
			what := "no alert"
			if det == 1 {
				what = fmt.Sprintf("alert in %s s (target %d s)", pyPctF(dl, 0), tgt)
			}
			tail := ""
			if cl != 1 {
				tail = ", not cleared"
			}
			ev.set(id, det == 1 && dl <= float64(tgt) && cl == 1, caseLabel(c)+": "+what+tail)
		}
	}
	if P.Truthy("telemetry") {
		t := r.M("telemetry")
		if !pyjson.Truthy(t) {
			ev.set("telemetry coverage", nil, "no TELEMETRY line: confirm every device reports to the collector")
		} else {
			ex, se := rzf(t.V("expected")), rzf(t.V("seen"))
			ms := PyStr(dget(t, "missing", "-"))
			miss := ms == "-" || ms == ""
			tail := ""
			if !miss {
				tail = "; missing " + pyHead(ms, 160)
			}
			ev.set("telemetry coverage", ex > 0 && se >= ex && miss, fmt.Sprintf("%s of %s devices reporting%s", pyPctD(se), pyPctD(ex), tail))
		}
	}
	return rzVerdict(ev, PyStr(dget(r, "by", "")), "alerts arrive and clear; every device reporting")
}

// gateKindEval is gate_kind_eval: the result-backed gate kinds in one place (the gate and the post-deployment report).
func (e *Estate) gateKindEval(kind string, planes []string) *pyjson.Map {
	switch kind {
	case "accept":
		return e.acceptEval()
	case "tuned":
		return e.tunedEval()
	case "inference":
		return e.inferenceEval()
	case "failover":
		return e.failoverEval(planes)
	case "soak":
		return e.soakEval()
	case "alerts":
		return e.alertsEval()
	}
	return e.isolationEval()
}

func (e *Estate) runRZ(kind string, r *pyjson.Map, sec map[string]string, logf string) (bool, string, error) {
	var devs []string
	e.Mu.Lock()
	for _, n := range pyjson.StrList(r.V("devices")) {
		if _, ok := e.Devs[n]; ok && e.flag(n) != "quarantined" {
			devs = append(devs, n)
		}
	}
	e.Mu.Unlock()
	var s0 *AccSnap
	if kind == "soak" && len(devs) > 0 {
		s0 = e.accSnap(devs)
	}
	rc, err := e.runExec(r, []string{"bash", "-c", e.sub(e.Conf.Str(kind+"_cmd"), sec)}, nil, logf, "")
	if err != nil {
		return false, "", err
	}
	txt := readAll(logf)
	var ok bool
	var msg string
	switch kind {
	case "soak":
		var sw *pyjson.Map
		if len(devs) > 0 {
			sums, unread := e.accSwitch(s0, e.accSnap(devs))
			seen := map[string]bool{}
			for _, t := range []string{"leaf", "spine"} {
				for k := range sums[t].seen {
					seen[k] = true
				}
			}
			sl := make([]string, 0, len(seen))
			for k := range seen {
				sl = append(sl, k)
			}
			sort.Strings(sl)
			sa := make([]any, len(sl))
			for i := range sl {
				sa[i] = sl[i]
			}
			sw = pyjson.O("drops", sums["leaf"].get("drops")+sums["spine"].get("drops"), "wd", sums["leaf"].get("wd")+sums["spine"].get("wd"),
				"seen", sa, "unread", int64(len(unread)))
		}
		ok, msg = e.SubmitSoakText(txt, "runner", sw)
	case "failover":
		ok, msg = e.SubmitFailoverText(txt, "runner")
	default:
		ok, msg = e.SubmitAlertsText(txt, "runner")
	}
	if !ok {
		return false, fmt.Sprintf("exit %d; %s", rc, msg), nil
	}
	e.Mu.Lock()
	var res *pyjson.Map
	switch kind {
	case "failover":
		res = e.failoverEval(nil)
	case "soak":
		res = e.soakEval()
	default:
		res = e.alertsEval()
	}
	lvl := "warn"
	if res.Str("result") == "pass" {
		lvl = "info"
	}
	e.ev(map[string]string{"failover": "Failover", "soak": "Soak", "alerts": "Alerts"}[kind]+": "+res.Str("note"), lvl, "")
	e.Mu.Unlock()
	return res.Str("result") == "pass", res.Str("note"), nil
}
