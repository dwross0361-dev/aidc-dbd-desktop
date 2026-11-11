package estate

// Results that gate checks read (NCCL, lossless acceptance T1-T4, ingress isolation), the tuning record's evaluation,
// and the lossless acceptance scoring (Python: submit_result, submit_*_text, accept_eval, isolation_eval, tuned_eval,
// _acc_score, _acc_switch, _acc_counts_of, tuning_values/lines/swap).

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"aidc-dbd/provgo/internal/pyjson"
)

// ------------------------------------------------------------------------------------- tuning overlay (read side)

func (e *Estate) tuningValues(v int64) *pyjson.Map {
	tp := e.tuningPlan()
	base := pyjson.NewMap()
	for _, p := range tp.L("params") {
		pm := p.(*pyjson.Map)
		base.Set(PyStr(pm.V("key")), pm.V("base"))
	}
	if v == 0 {
		return base
	}
	for _, x := range e.tuningState().L("versions") {
		xm := x.(*pyjson.Map)
		if pyjson.Float(xm.V("v")) == float64(v) {
			out := base.Copy()
			if vm := xm.M("values"); vm != nil {
				for _, k := range vm.Keys() {
					out.Set(k, vm.V(k))
				}
			}
			return out
		}
	}
	return nil
}

// tfmt is _tfmt: an integral value without a fraction, else %g.
func tfmt(x float64) string {
	if x == math.Trunc(x) && !math.IsInf(x, 0) {
		return strconv.FormatFloat(x, 'f', 0, 64)
	}
	return pyPctG(x)
}

func pyRound(x float64, nd int) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	f, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', nd, 64), 64)
	return f
}

var tuneTokRE = regexp.MustCompile(`\{([A-Z0-9_]+)(?:([*/])(\d+(?:\.\d+)?))?\}`)

func (e *Estate) tuningLines(values *pyjson.Map) ([]string, error) {
	var out []string
	var ferr error
	for _, l := range pyjson.StrList(e.tuningPlan().V("lines")) {
		out = append(out, replaceSubmatchFunc(tuneTokRE, l, func(m []string) string {
			v, ok := values.Get(m[1])
			if !ok {
				ferr = fmt.Errorf("%s", pyStrRepr(m[1]))
				return ""
			}
			x := pyFloatAnyZ(v)
			if m[2] != "" {
				f, _ := strconv.ParseFloat(m[3], 64)
				if m[2] == "*" {
					x *= f
				} else {
					x /= f
				}
			}
			return tfmt(pyRound(x, 3))
		}))
	}
	return out, ferr
}

var (
	tuneSkipRE = regexp.MustCompile(`^(delete |exit$|nv config )`)
	tuneCutRE  = pyRe(`\s+!\s.*$`)
)

// tuningSwap is the planned lossless lines with the switch's active overlay in place of the package's own values.
func (e *Estate) tuningSwap(n string, planned StrSet, os_ string) StrSet {
	t := e.tuningState()
	v := int64(0)
	if d := t.M("dev").M(n); d != nil {
		v = pyjson.Int(d.V("v"))
	}
	if v == 0 || !e.tuningPlan().Truthy("lines") {
		return planned
	}
	keep := func(L []string) StrSet {
		out := StrSet{}
		for x := range CfgNorm(strings.Join(L, "\n"), os_) {
			if !tuneSkipRE.MatchString(x) {
				out.add(x)
			}
		}
		return out
	}
	bl, _ := e.tuningLines(e.tuningValues(0))
	ov := e.tuningValues(v)
	if ov == nil {
		ov = pyjson.NewMap()
	}
	ol, err := e.tuningLines(ov)
	if err != nil {
		panic(err) // Python: KeyError, the device's check reads "read failed"
	}
	b, o := keep(bl), keep(ol)
	cut := func(x string) string { return tuneCutRE.ReplaceAllString(x, "") }
	gone := StrSet{}
	for x := range b {
		if _, in := o[x]; !in {
			gone.add(cut(x))
		}
	}
	out := StrSet{}
	for x := range planned {
		if _, g := gone[cut(x)]; !g {
			out.add(x)
		}
	}
	for x := range o {
		out.add(x)
	}
	return out
}

// tunedEval is the "every back-end switch at the approved tuning" check.
func (e *Estate) tunedEval() *pyjson.Map {
	tp, t := e.tuningPlan(), e.tuningState()
	every := e.tuningScope("all")
	if !tp.Truthy("params") {
		return pyjson.O("result", "pass", "note", "no tunable values on this platform", "evidence", pyjson.NewMap())
	}
	if len(every) == 0 {
		return pyjson.O("result", "pass", "note", "no lossless back-end switches in this build", "evidence", pyjson.NewMap())
	}
	ap := t.M("approved")
	versions := t.L("versions")
	if len(versions) == 0 && !pyjson.Truthy(t.V("approved")) {
		return pyjson.O("result", "needs confirmation", "note", "no tuning change: approve the package values (v0) once acceptance passes", "evidence", pyjson.NewMap())
	}
	if !pyjson.Truthy(t.V("approved")) {
		return pyjson.O("result", "needs confirmation", "note", fmt.Sprintf("%d tuning version(s), none approved: approve one (or v0, the package values)", len(versions)), "evidence", pyjson.NewMap())
	}
	v := pyjson.Float(ap.V("v"))
	dev := t.M("dev")
	dv := func(n string) float64 {
		if d := dev.M(n); d != nil && d.Has("v") {
			return pyjson.Float(d.V("v"))
		}
		return 0
	}
	at := 0
	ev := pyjson.NewMap()
	for _, n := range every {
		if dv(n) == v {
			at++
			continue
		}
		note := "v" + pyPctD(dv(n))
		if d := dev.M(n); d != nil && d.Has("ok") && !pyjson.Truthy(d.V("ok")) {
			note += " (" + PyStr(dget(d, "note", "")) + ")"
		}
		ev.Set(n, pyjson.O("ok", false, "note", note))
	}
	ro := t.M("rollout")
	ros := ""
	if pyjson.Truthy(t.V("rollout")) {
		ros = "; rollout " + PyStr(dget(ro, "state", ""))
	}
	if at == len(every) {
		who := "package values"
		if v != 0 {
			who = "approved by " + PyStr(ap.V("by"))
		}
		return pyjson.O("result", "pass", "note", fmt.Sprintf("every back-end switch at v%s (%s)%s", pyPctD(v), who, ros), "evidence", ev)
	}
	rs := "running"
	if ro != nil && strings.Contains(PyStr(dget(ro, "state", "")), "stopped") {
		rs = "blocked"
	}
	return pyjson.O("result", rs, "note", fmt.Sprintf("%d of %d back-end switches at approved v%s%s", at, len(every), pyPctD(v), ros), "evidence", ev)
}

// ------------------------------------------------------------------------------------- NCCL

// SubmitResult records a bus-bandwidth result (NCCL; the CLI, the console or the runner). busbw and target are the
// values as given (the audit line shows them as given; the record keeps them as numbers).
func (e *Estate) SubmitResult(kind string, busbw, target any, user, text string) (bool, string) {
	b, ok1 := pyFloatAny(busbw)
	t, ok2 := pyFloatAny(target)
	if !ok1 || !ok2 {
		panic(errors.New("could not convert string to float"))
	}
	e.Mu.Lock()
	defer e.Mu.Unlock()
	e.Results.Set(kind, pyjson.O("busbw", b, "target", t, "by", user, "ts", Now(), "text", text))
	e.KVSet("results", e.Results)
	e.audit(user, "result-"+kind, "", fmt.Sprintf("%s GB/s against %s", PyStr(busbw), PyStr(target)))
	e.autoPopKind(kind)
	return true, "recorded"
}

// ------------------------------------------------------------------------------------- ingress isolation

var isoRE = pyRe(`(?m)^ISOLATION\s+(\S+)\s+(bind|snat|cross|setup)\s+(PASS|FAIL)\s*(.*)$`)

// SubmitIsolationText records the ingress isolation script's output (pasted in the console, or the runner's log).
func (e *Estate) SubmitIsolationText(text, user string) (bool, string) {
	T := pyjson.NewMap()
	for _, m := range isoRE.FindAllStringSubmatch(text, -1) {
		tm, _ := T.V(m[1]).(*pyjson.Map)
		if tm == nil {
			tm = pyjson.NewMap()
			T.Set(m[1], tm)
		}
		tm.Set(m[2], pyjson.O("ok", m[3] == "PASS", "note", pyHead(pyStripU(m[4]), 300)))
	}
	if T.Len() == 0 {
		return false, "no ISOLATION lines in the text (paste the output of acceptance/ingress_isolation.sh)"
	}
	tests := pyjson.NewMap()
	failing := 0
	for _, t := range T.Keys() {
		v := T.M(t)
		all := true
		for _, k := range v.Keys() {
			if !v.M(k).Truthy("ok") {
				all = false
			}
		}
		r := "blocked"
		if all {
			r = "pass"
		} else {
			failing++
		}
		tests.Set(t, pyjson.O("result", r, "checks", v))
	}
	e.Mu.Lock()
	defer e.Mu.Unlock()
	e.Results.Set("isolation", pyjson.O("tests", tests, "by", user, "ts", Now()))
	e.KVSet("results", e.Results)
	e.audit(user, "result-isolation", "", fmt.Sprintf("%d tenant(s), %d failing", tests.Len(), failing))
	e.autoPopKind("isolation")
	return true, "recorded"
}

func (e *Estate) isolationEval() *pyjson.Map {
	P := e.Plan.M("isolation")
	var want []string
	if P != nil {
		for _, x := range P.L("tenants") {
			want = append(want, PyStr(x.(*pyjson.Map).V("tenant")))
		}
	}
	checks := []string{"bind", "snat"}
	if len(want) > 1 {
		checks = append(checks, "cross")
	}
	r := e.Results.M("isolation")
	if !pyjson.Truthy(r) {
		return pyjson.O("result", "waiting", "note", "waiting for the ingress isolation run (runner or uploaded results)", "evidence", pyjson.NewMap())
	}
	T := r.M("tests")
	if T == nil {
		T = pyjson.NewMap()
	}
	ev := pyjson.NewMap()
	var bad, missing []string
	ts := want
	if len(ts) == 0 {
		ts = append([]string(nil), T.Keys()...)
		sort.Strings(ts)
	}
	for _, t := range ts {
		var got *pyjson.Map
		if tm := T.M(t); tm != nil {
			got = tm.M("checks")
		}
		if got == nil {
			got = pyjson.NewMap()
		}
		ks := append([]string(nil), checks...)
		if got.Has("setup") {
			ks = append(ks, "setup")
		}
		for _, k := range ks {
			if !got.Has(k) {
				missing = append(missing, t+" "+k)
				continue
			}
			g := got.M(k)
			ev.Set(t+" "+k, pyjson.O("ok", g.V("ok"), "note", g.V("note")))
			if !g.Truthy("ok") {
				bad = append(bad, t+" "+k)
			}
		}
	}
	by := PyStr(dget(r, "by", ""))
	if len(bad) > 0 {
		sh := bad
		if len(sh) > 4 {
			sh = sh[:4]
		}
		more := ""
		if len(bad) > 4 {
			more = fmt.Sprintf(" +%d more", len(bad)-4)
		}
		return pyjson.O("result", "blocked", "note", fmt.Sprintf("%d failing: %s (%s)", len(bad), strings.Join(sh, ", ")+more, by), "evidence", ev)
	}
	if len(missing) > 0 {
		sh := missing
		if len(sh) > 4 {
			sh = sh[:4]
		}
		return pyjson.O("result", "needs confirmation", "note", fmt.Sprintf("%d check(s) not reported: %s (%s)", len(missing), strings.Join(sh, ", "), by), "evidence", ev)
	}
	n := len(want)
	if n == 0 {
		n = T.Len()
	}
	cross := ""
	if containsStr(checks, "cross") {
		cross = ", no cross-tenant reach"
	}
	return pyjson.O("result", "pass", "note", fmt.Sprintf("%d tenant(s): VIP bound to its namespace, own SNAT%s (%s)", n, cross, by), "evidence", ev)
}

// ------------------------------------------------------------------------------------- lossless acceptance

// accSnapEntry is one switch's counters or why they could not be read.
type accSnapEntry struct {
	c   *AccCount
	err string
}

// AccSnap is the counters of a set of switches, keyed and ordered by device.
type AccSnap struct {
	keys []string
	m    map[string]accSnapEntry
}

func (e *Estate) accCountsOf(n string) (*AccCount, error) {
	os_ := e.Devs[n].Str("os")
	cmds := CNTCMDS[os_]
	if len(cmds) == 0 {
		return nil, unavailable("%s", "no counter reader for "+os_)
	}
	var exp []string
	for _, c := range cmds {
		if strings.HasPrefix(c, "@per-if:") {
			pl, _ := e.plannedText(n)
			set := StrSet{}
			for _, m := range pyRe(`nv set interface (\S+) qos pfc-watchdog`).FindAllStringSubmatch(pl, -1) {
				set.add(m[1])
			}
			for _, i := range set.Sorted() {
				exp = append(exp, strings.ReplaceAll(c[8:], "{if}", i))
			}
		} else {
			exp = append(exp, c)
		}
	}
	tot := newAccCount()
	outs, err := e.Read(n, exp)
	if err != nil {
		return nil, err
	}
	for _, t := range outs {
		c := AccCounts(AccFlatText(t))
		for _, k := range accKeys {
			tot.add(k, c.get(k))
		}
		for s := range c.Seen {
			tot.Seen[s] = true
		}
	}
	return tot, nil
}

// accSnap reads the counters of every switch (reader_workers in parallel).
func (e *Estate) accSnap(devs []string) *AccSnap {
	out := make([]accSnapEntry, len(devs))
	e.poolMap(len(devs), func(i int) {
		c, err := e.accCountsOf(devs[i])
		if err != nil {
			out[i] = accSnapEntry{nil, err.Error()}
			return
		}
		out[i] = accSnapEntry{c, ""}
	})
	s := &AccSnap{m: map[string]accSnapEntry{}}
	for i, n := range devs {
		if _, ok := s.m[n]; !ok {
			s.keys = append(s.keys, n)
		}
		s.m[n] = out[i]
	}
	return s
}

var accSpineRE, accSpineNameRE = regexp.MustCompile(`(?i)spine|super|ssp`), regexp.MustCompile(`-S(P|SP)\d`)

func (e *Estate) accRole(n string) string {
	role := ""
	if d := e.Devs[n]; d != nil {
		role = PyStr(dget(d, "role", ""))
	}
	if accSpineRE.MatchString(role) || accSpineNameRE.MatchString(n) {
		return "spine"
	}
	return "leaf"
}

// accTier is one tier's counter changes; keys in Python's insertion order.
type accTier struct {
	keys []string
	m    map[string]float64
	seen map[string]bool
	has  bool // "seen" key set
}

func (t *accTier) addv(k string, v float64) {
	if _, ok := t.m[k]; !ok {
		t.keys = append(t.keys, k)
	}
	t.m[k] += v
}

func (t *accTier) get(k string) float64 { return t.m[k] }

// accSwitch is the counter changes over one test, by tier, and the switches not read.
func (e *Estate) accSwitch(s0, s1 *AccSnap) (map[string]*accTier, []string) {
	sums := map[string]*accTier{"leaf": {m: map[string]float64{}, seen: map[string]bool{}}, "spine": {m: map[string]float64{}, seen: map[string]bool{}}}
	var unread []string
	if s1 == nil {
		return sums, unread
	}
	for _, n := range s1.keys {
		e1 := s1.m[n]
		var e0 accSnapEntry
		if s0 != nil {
			e0 = s0.m[n]
		}
		if e0.c == nil || e1.c == nil {
			why := e1.err
			if why == "" {
				why = e0.err
			}
			if why == "" {
				why = "not read"
			}
			unread = append(unread, fmt.Sprintf("%s: %s", e.devShort(n, n), why))
			continue
		}
		t := sums[e.accRole(n)]
		for _, k := range accKeys {
			d := e1.c.get(k) - e0.c.get(k)
			if d >= 0 {
				t.addv(k, d)
			} else {
				t.addv(k, e1.c.get(k))
			}
		}
		t.has = true
		for s := range e1.c.Seen {
			if e0.c.Seen[s] {
				t.seen[s] = true
			}
		}
	}
	return sums, unread
}

// accScore scores one acceptance test from the host script's text and the switch counters before and after.
// s0 and s1 are nil when the text was uploaded by hand. rc < 0 means "no exit code" (Python None).
func (e *Estate) accScore(test, text string, s0, s1 *AccSnap, crit *pyjson.Map, rc int) *pyjson.Map {
	kv, nic := AccParse(text, test)
	sums, unread := e.accSwitch(s0, s1)
	L := []any{}
	line := func(label string, ok any, note string) {
		L = append(L, pyjson.O("label", test+" "+label, "ok", ok, "note", note))
	}
	seen := map[string]bool{}
	for _, t := range []string{"leaf", "spine"} {
		for s := range sums[t].seen {
			seen[s] = true
		}
	}
	tot := func(k string) float64 { return sums["leaf"].get(k) + sums["spine"].get(k) }
	kvf := func(k string) float64 {
		v := kv.V(k)
		if !pyjson.Truthy(v) {
			return 0
		}
		f, ok := pyFloatAny(v)
		if !ok {
			panic(fmt.Errorf("could not convert string to float: %s", pyStrRepr(PyStr(v))))
		}
		return f
	}
	critf := func(k string, def float64) float64 {
		if v, ok := crit.Get(k); ok {
			return pyFloatAnyZ(v)
		}
		return def
	}
	if rc != 0 && rc >= 0 && kv.Len() == 0 {
		line("ran", false, fmt.Sprintf("the test exited %d with no result line", rc))
	}
	if s1 != nil {
		if seen["drops"] {
			line("no drops on the RoCE queue (switches)", tot("drops") == 0, fmt.Sprintf("%s dropped", pyPctD(tot("drops"))))
		} else {
			line("no drops on the RoCE queue (switches)", nil, "drop counters not understood: confirm by hand")
		}
		if seen["wd"] {
			line("no PFC watchdog events", tot("wd") == 0, fmt.Sprintf("%s events", pyPctD(tot("wd"))))
		} else {
			line("no PFC watchdog events", nil, "watchdog counters not understood: confirm by hand")
		}
	}
	if len(nic.Keys) > 0 {
		bad := nic.M["rx_discards_phy"] + nic.M["out_of_sequence"] + nic.M["packet_seq_err"]
		line("no loss at the hosts (NIC discards, sequence errors)", bad == 0, pyPctD(bad))
	}
	pfc := tot("pfc_tx") + tot("pfc_rx")
	switch test {
	case "T1":
		r, mn := kvf("port_gbps"), kvf("min_gbps")
		pct := critf("line_rate_pct", 95)
		var ok any
		if r != 0 && kv.Has("min_gbps") {
			ok = mn >= pct/100.0*r
		}
		note := "no result line from the host script"
		if r != 0 {
			note = fmt.Sprintf("slowest path %s of %s Gb/s (target %s%%)", pyPctF(mn, 1), pyPctF(r, 0), pyPctD(pct))
		}
		line("line rate on every path", ok, note)
		if seen["pfc"] {
			line("no PFC or ECN activity at rest load", pfc == 0 && tot("ecn") == 0, fmt.Sprintf("%s pause, %s marked", pyPctD(pfc), pyPctD(tot("ecn"))))
		}
	case "T2":
		if seen["pfc"] {
			marks := tot("ecn") + nic.M["np_ecn_marked_roce_packets"]
			var ok any = true
			if pfc > 0 {
				ok = marks > 0
			}
			line("ECN marks before PFC pauses", ok, fmt.Sprintf("%s pause frames, %s ECN marks", pyPctD(pfc), pyPctD(marks)))
		} else if s1 != nil {
			line("ECN marks before PFC pauses", nil, "PFC counters not understood: confirm by hand")
		}
		mn, mx, mean := kvf("min_gbps"), kvf("max_gbps"), kvf("mean_gbps")
		fair := critf("fairness_pct", 10)
		var ok any
		note := "no result line from the host script"
		if mean != 0 {
			ok = 100.0*(mx-mn)/mean <= fair
			note = fmt.Sprintf("spread %s%% (target %s%%)", pyPctF(100.0*(mx-mn)/mean, 1), pyPctD(fair))
		}
		line("senders share the receiver fairly", ok, note)
	case "T3":
		sp := sums["spine"]
		if s1 != nil {
			if sp.seen["pfc"] {
				line("no PFC at the spines under all-to-all", sp.get("pfc_tx")+sp.get("pfc_rx") == 0, fmt.Sprintf("%s pause frames at the spines", pyPctD(sp.get("pfc_tx")+sp.get("pfc_rx"))))
			} else {
				line("no PFC at the spines under all-to-all", nil, "spine PFC counters not understood: confirm by hand")
			}
		}
		var ok any
		if kvf("busbw") > 0 {
			ok = true
		}
		bb := "?"
		if v, have := kv.Get("busbw"); have {
			bb = pyStrNum(v)
		}
		line("all-to-all completed", ok, "bus bandwidth "+bb+" GB/s")
	case "T4":
		tgt := 0.0
		if v := e.Conf.V("TARGET_BUSBW"); pyjson.Truthy(v) {
			tgt = pyFloatAnyZ(v)
		} else if s := e.Secrets()["TARGET_BUSBW"]; s != "" {
			tgt = pyFloatAnyZ(s)
		}
		b := kvf("busbw")
		var ok any
		if tgt != 0 && b != 0 {
			ok = b >= tgt
		}
		note := fmt.Sprintf("TARGET_BUSBW not set: confirm by hand (%s GB/s)", pyPctF(b, 1))
		if tgt != 0 {
			note = fmt.Sprintf("%s GB/s against %s", pyPctF(b, 1), pyPctF(tgt, 1))
		}
		line("bus bandwidth at target", ok, note)
	case "T7": // HS v2.03: elephant flows across the spines (dynamic load balancing / adaptive routing at work)
		r, mn, mx, mean := kvf("port_gbps"), kvf("min_gbps"), kvf("max_gbps"), kvf("mean_gbps")
		ov := critf("oversub", 1)
		if ov == 0 {
			ov = 1
		}
		pct, spr := critf("elephant_pct", 90), critf("ecmp_spread_pct", 10)
		var ok any
		if r != 0 && kv.Has("min_gbps") {
			ok = mn >= pct/100.0*r/ov
		}
		note := "no result line from the host script"
		if r != 0 {
			note = fmt.Sprintf("slowest of %s flows %s of %s Gb/s (target %s%%)", pyPctD(kvf("flows")), pyPctF(mn, 1), pyPctF(r/ov, 0), pyPctD(pct))
		}
		line("every elephant flow across the spines near line rate", ok, note)
		var ok2 any
		note2 := "no result line from the host script"
		if mean != 0 {
			ok2 = 100.0*(mx-mn)/mean <= spr
			note2 = fmt.Sprintf("spread %s%% (target %s%%)", pyPctF(100.0*(mx-mn)/mean, 1), pyPctD(spr))
		}
		line("flows spread evenly over the uplinks", ok2, note2)
	}
	if len(unread) > 0 {
		sh := unread
		if len(sh) > 3 {
			sh = sh[:3]
		}
		more := ""
		if len(unread) > 3 {
			more = fmt.Sprintf(" +%d more", len(unread)-3)
		}
		line("switch counters read", nil, strings.Join(sh, "; ")+more)
	}
	res := "pass"
	for _, x := range L {
		if v, isB := x.(*pyjson.Map).V("ok").(bool); isB && !v {
			res = "blocked"
			break
		}
	}
	if res == "pass" {
		for _, x := range L {
			if x.(*pyjson.Map).V("ok") == nil {
				res = "unavailable"
				break
			}
		}
	}
	sw := pyjson.NewMap()
	for _, tn := range []string{"leaf", "spine"} {
		tm := pyjson.NewMap()
		for _, k := range sums[tn].keys {
			tm.Set(k, sums[tn].m[k])
		}
		sw.Set(tn, tm)
	}
	nm := pyjson.NewMap()
	for _, k := range nic.Keys {
		nm.Set(k, nic.M[k])
	}
	return pyjson.O("result", res, "lines", L, "switch", sw, "nic", nm, "host", kv)
}

// pyStrNum is str() of a value parsed from a result line (a float, or the text as given).
func pyStrNum(v any) string {
	if f, ok := v.(float64); ok {
		return pyFloatStr(f)
	}
	return PyStr(v)
}

var acceptLineRE = map[string]*regexp.Regexp{}

// SubmitAcceptText scores the host script's output pasted in the console (without the switch counters).
func (e *Estate) SubmitAcceptText(text, user string, crit *pyjson.Map) (bool, string) {
	if crit == nil {
		crit = pyjson.NewMap()
	}
	tests := pyjson.NewMap()
	for _, t := range []string{"T1", "T2", "T3", "T4", "T7"} {
		if pyRe(`(?m)^ACCEPT\s+` + t + `\b`).MatchString(text) {
			r := e.accScore(t, text, nil, nil, crit, 0)
			r.Set("lines", append(r.L("lines"), pyjson.O("label", t+" switch counters", "ok", nil, "note", "uploaded by hand: confirm the switch criteria from the switches")))
			if r.Str("result") == "pass" {
				r.Set("result", "unavailable")
			}
			tests.Set(t, r)
		}
	}
	if tests.Len() == 0 {
		return false, "no ACCEPT lines in the text (paste the output of acceptance/lossless.sh)"
	}
	return e.SubmitAcceptResult(tests, user)
}

// SubmitAcceptResult records the acceptance tests' scores.
func (e *Estate) SubmitAcceptResult(tests *pyjson.Map, user string) (bool, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	e.Results.Set("accept", pyjson.O("tests", tests, "by", user, "ts", Now()))
	e.KVSet("results", e.Results)
	ks := append([]string(nil), tests.Keys()...)
	sort.Strings(ks)
	var parts []string
	for _, t := range ks {
		parts = append(parts, t+" "+PyStr(tests.M(t).V("result")))
	}
	e.audit(user, "result-accept", "", strings.Join(parts, ", "))
	e.autoPopKind("accept")
	return true, "recorded"
}

// accTests is _acc_tests: the tests the plan's acceptance runner requires (HS v2.03: T7 where the back end has two pods).
func (e *Estate) accTests() []string {
	for _, rv := range e.Plan.L("runners") {
		r, ok := rv.(*pyjson.Map)
		if !ok || r.Str("kind") != "accept" {
			continue
		}
		if t := pyjson.StrList(r.V("tests")); len(t) > 0 {
			return t
		}
		return []string{"T1", "T2", "T3", "T4"}
	}
	return []string{"T1", "T2", "T3", "T4"}
}

func (e *Estate) acceptEval() *pyjson.Map {
	r := e.Results.M("accept")
	if !pyjson.Truthy(r) {
		return pyjson.O("result", "waiting", "note", "waiting for the lossless acceptance run (runner or uploaded results)", "evidence", pyjson.NewMap())
	}
	T := r.M("tests")
	if T == nil {
		T = pyjson.NewMap()
	}
	ks := append([]string(nil), T.Keys()...)
	sort.Strings(ks)
	ev := pyjson.NewMap()
	for _, t := range ks {
		for _, x := range T.M(t).L("lines") {
			xm := x.(*pyjson.Map)
			if xm.V("ok") != nil {
				ev.Set(PyStr(xm.V("label")), pyjson.O("ok", pyjson.Truthy(xm.V("ok")), "note", xm.V("note")))
			} else {
				ev.Set(PyStr(xm.V("label")), pyjson.O("ok", false, "note", "confirm: "+PyStr(xm.V("note"))))
			}
		}
	}
	var res []string
	for _, t := range T.Keys() {
		res = append(res, PyStr(T.M(t).V("result")))
	}
	var missing []string
	for _, t := range e.accTests() {
		if !T.Has(t) {
			missing = append(missing, t)
		}
	}
	overall := "pass"
	if containsStr(res, "blocked") {
		overall = "blocked"
	} else if containsStr(res, "unavailable") || len(missing) > 0 {
		overall = "unavailable"
	}
	var parts []string
	for _, t := range ks {
		parts = append(parts, t+" "+PyStr(T.M(t).V("result")))
	}
	note := strings.Join(parts, ", ")
	if len(missing) > 0 {
		note += " (not run: " + strings.Join(missing, ", ") + ")"
	}
	note += " (" + PyStr(dget(r, "by", "")) + ")"
	return pyjson.O("result", overall, "note", note, "evidence", ev)
}
