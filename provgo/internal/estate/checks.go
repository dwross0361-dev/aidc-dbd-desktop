package estate

// Automatic gate checks (Python: Phase2Mixin.eval_kind, run_auto, checker, gate_recheck). Device reads run without the
// estate lock; every look at device state or a stored result takes it briefly.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"aidc-dbd/provgo/internal/pyjson"
)

// p2 holds the Phase 2 in-memory caches (per process, as in Python).
type p2 struct {
	mu        sync.Mutex
	peers     map[string]map[string]string
	exp       map[string]*orderedLinks
	short2nm  map[string]string
	idlePrev  map[[2]string]map[string]bool
	probeDue  map[string]float64
	downMiss  map[string]int
	backupDue map[string]float64
}

type orderedLinks struct {
	keys []string
	m    map[string][2]string
}

func (e *Estate) p2s() *p2 {
	e.p2once.Do(func() {
		e.p2c = &p2{peers: map[string]map[string]string{}, exp: map[string]*orderedLinks{}, idlePrev: map[[2]string]map[string]bool{},
			probeDue: map[string]float64{}, downMiss: map[string]int{}, backupDue: map[string]float64{}}
	})
	return e.p2c
}

// ------------------------------------------------------------------------------------- planned configuration

// plannedText is the device's planned configuration (bundle_dir, then web_root); Cumulus keeps its nv lines only.
func (e *Estate) plannedText(n string) (string, bool) {
	d := e.Devs[n]
	if d == nil || !d.Truthy("planned_config") {
		return "", false
	}
	p := PyStr(d.V("planned_config"))
	t, err := readTextPy(filepath.Join(e.Conf.Str("bundle_dir"), p))
	if err != nil {
		t, err = readTextPy(filepath.Join(e.Conf.Str("web_root"), p))
		if err != nil {
			return "", false
		}
	}
	if d.Str("os") == "cumulus" {
		var keep []string
		for _, l := range pySplitlines(t) {
			if strings.HasPrefix(l, "nv ") {
				keep = append(keep, l)
			}
		}
		t = strings.Join(keep, "\n")
	}
	return t, true
}

var (
	peerRE = pyRe(`neighbor (\S+) remote-as \S+\s+#\s*([A-Za-z0-9_.-]+)`)
	linkRE = pyRe(`interface (\S+) link mtu \d+\s+#\s*->\s*(\S+)\s+(\S+)`)
)

// peersOf is each BGP neighbor's device, from the planned configuration's "neighbor X remote-as N  # PEER" comments.
func (e *Estate) peersOf(n string) map[string]string {
	c := e.p2s()
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.peers[n]; ok {
		return m
	}
	m := map[string]string{}
	t, _ := e.plannedText(n)
	for _, x := range peerRE.FindAllStringSubmatch(t, -1) {
		m[x[1]] = x[2]
	}
	c.peers[n] = m
	return m
}

func (e *Estate) peerName(x string) string {
	if x == "" || e.Devs[x] != nil {
		return x
	}
	c := e.p2s()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.short2nm == nil {
		c.short2nm = map[string]string{}
		for _, k := range e.DevOrder {
			d := e.Devs[k]
			s := k
			if d.Has("short") {
				s = PyStr(d.V("short"))
			}
			c.short2nm[s] = k
		}
	}
	if v, ok := c.short2nm[x]; ok {
		return v
	}
	return x
}

// expectedLinks is the cable plan of a device: the planned configuration's "interface X link mtu N  # -> PEER PORT".
func (e *Estate) expectedLinks(n string) *orderedLinks {
	c := e.p2s()
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.exp[n]; ok {
		return m
	}
	m := &orderedLinks{m: map[string][2]string{}}
	t, _ := e.plannedText(n)
	for _, x := range linkRE.FindAllStringSubmatch(t, -1) {
		if _, ok := m.m[x[1]]; !ok {
			m.keys = append(m.keys, x[1])
		}
		m.m[x[1]] = [2]string{x[2], x[3]}
	}
	c.exp[n] = m
	return m
}

func (e *Estate) bgpTexts(name, kind string) ([]string, error) {
	os_ := e.Devs[name].Str("os")
	cmds := CMDS[kind][os_]
	if len(cmds) == 0 {
		return nil, unavailable("no %s check for %s", kind, os_)
	}
	if len(cmds) == 1 && cmds[0] == "@nvue-bgp" {
		vr, err := e.Read(name, []string{"nv show vrf -o json"})
		if err != nil {
			return nil, err
		}
		vrfs := []string{"default"}
		if obj, err := pyjson.Loads([]byte(vr[0])); err == nil {
			m, ok := obj.(*pyjson.Map)
			if !ok {
				return nil, fmt.Errorf("'%s' object has no attribute 'keys'", pyTypeName(obj))
			}
			vrfs = append([]string(nil), m.Keys()...)
			sort.Strings(vrfs)
		}
		cc := make([]string, len(vrfs))
		for i, x := range vrfs {
			cc[i] = "nv show vrf " + x + " router bgp neighbor -o json"
		}
		out, err := e.Read(name, cc)
		if err != nil {
			return nil, err
		}
		var keep []string
		for _, t := range out {
			if !strings.HasPrefix(pyStripU(t), "Error:") {
				keep = append(keep, t)
			}
		}
		if len(keep) == 0 {
			return out, nil
		}
		return keep, nil
	}
	return e.Read(name, cmds)
}

func pyTypeName(v any) string {
	switch v.(type) {
	case []any:
		return "list"
	case string:
		return "str"
	case int64:
		return "int"
	case float64:
		return "float"
	case bool:
		return "bool"
	case nil:
		return "NoneType"
	case *pyjson.Map:
		return "dict"
	}
	return "object"
}

// ------------------------------------------------------------------------------------- one device, one check

func (e *Estate) stOf(n string) *pyjson.Map {
	if s := e.St[n]; s != nil {
		return s
	}
	return pyjson.NewMap()
}

func (e *Estate) devShort(n, def string) string {
	if d := e.Devs[n]; d != nil {
		if d.Has("short") {
			return PyStr(d.V("short"))
		}
	}
	return def
}

var (
	mnhaRE      = regexp.MustCompile(`MNHA`)
	primaryRE   = pyRe(`\bprimary\b`)
	secondaryRE = pyRe(`\bsecondary\b`)
	roceCumRE   = pyRe(`"mode"\s*:\s*"lossless"`)
	roceJPfcRE  = regexp.MustCompile(`pfc|priority-flow-control`)
	roceJEcnRE  = regexp.MustCompile(`explicit-congestion|ecn`)
	roceOnRE    = pyRe(`\b(Enabled|On)\b`)
	roceOffRE   = pyRe(`\bDisabled\b.*\bDisabled\b`)
	verTokRE    = pyRe(`\d+[\d.()A-Za-z-]*\d`)
	distribRE   = pyRe(`DISTRIB_RELEASE=(\S+)`)
	verWordRE   = pyRe(`(?:version|Version|VERSION)[^\n]{0,20}?(\d+[\w.()-]*)`)
	ftdActRE    = map[bool]*regexp.Regexp{true: pyRe(`This host:\s*(Primary|Secondary)\s*-\s*Active`), false: pyRe(`This host:\s*(Primary|Secondary)\s*-\s*Standby Ready`)}
	srgRE       = map[bool]*regexp.Regexp{true: pyRe(`(?i)Services Redundancy Group:\s*1\b[\s\S]{0,800}?(Current State|State)\s*:\s*ACTIVE`),
		false: pyRe(`(?i)Services Redundancy Group:\s*1\b[\s\S]{0,800}?(Current State|State)\s*:\s*BACKUP`)}
)

// EvalKind runs one automatic check on one device: (ok, note, text). text may carry "\x00RAW\n" and the raw output.
func (e *Estate) EvalKind(kind, n string, c *pyjson.Map) (bool, string, string, error) {
	os_ := e.Devs[n].Str("os")
	switch kind {
	case "bgp", "evpn":
		texts, err := e.bgpTexts(n, kind)
		if err != nil {
			return false, "", "", err
		}
		sess0 := BGPSessions(texts)
		peers := e.peersOf(n)
		e.Mu.Lock()
		var aside []string
		for _, x := range sess0 {
			if PyStr(e.stOf(e.peerName(peers[x.Peer])).V("flag")) == "quarantined" {
				aside = append(aside, x.Peer)
			}
		}
		e.Mu.Unlock()
		var sess []BGPSess
		for _, x := range sess0 {
			if !containsStr(aside, x.Peer) {
				sess = append(sess, x)
			}
		}
		var est []string
		var down []BGPSess
		for _, x := range sess {
			if strings.HasPrefix(pyLower(x.State), "established") {
				est = append(est, x.Peer)
			} else {
				down = append(down, x)
			}
		}
		pc := e.p2s()
		pc.mu.Lock()
		key := [2]string{n, kind}
		prev, known := pc.idlePrev[key]
		cur := map[string]bool{}
		for _, x := range down {
			cur[x.Peer] = true
		}
		pc.idlePrev[key] = cur
		pc.mu.Unlock()
		var newd []BGPSess
		for _, x := range down {
			if known && !prev[x.Peer] {
				newd = append(newd, x)
			}
		}
		isNew := func(x BGPSess) bool {
			for _, y := range newd {
				if y == x {
					return true
				}
			}
			return false
		}
		order := append([]BGPSess(nil), newd...)
		for _, x := range down {
			if !isNew(x) {
				order = append(order, x)
			}
		}
		need := int64(1)
		if v, ok := c.Get("min"); ok {
			need = pyIntAny(v)
		}
		ok := int64(len(sess)) >= need && len(est) == len(sess)
		if len(sess) == 0 && len(aside) > 0 {
			return true, fmt.Sprintf("no session to check: all %d toward set-aside peers", len(aside)),
				fmt.Sprintf("set aside: %d session(s)\n\x00RAW\n%s", len(aside), strings.Join(texts, "\n")), nil
		}
		note := fmt.Sprintf("%d of %d sessions established", len(est), len(sess))
		if len(aside) > 0 {
			note += fmt.Sprintf(" (%d toward set-aside peers not counted)", len(aside))
		}
		if len(down) > 0 {
			var parts []string
			for i, x := range order {
				if i >= 6 {
					break
				}
				nw := ""
				if isNew(x) {
					nw = " (new)"
				}
				parts = append(parts, x.Peer+" "+pyLower(x.State)+nw)
			}
			more := ""
			if len(down) > 6 {
				more = fmt.Sprintf(" +%d more", len(down)-6)
			}
			note += fmt.Sprintf("; %d down: %s%s", len(down), strings.Join(parts, ", "), more)
		}
		pn := func(p string) string {
			if d := e.Devs[e.peerName(peers[p])]; d != nil && d.Truthy("short") {
				return PyStr(d.V("short"))
			}
			if v := peers[p]; v != "" {
				return v
			}
			return "?"
		}
		var summ []string
		for _, x := range order {
			nw := ""
			if isNew(x) {
				nw = " (new)"
			}
			summ = append(summ, fmt.Sprintf("%-10s %-28s %s", x.Peer, pn(x.Peer), pyLower(x.State)+nw))
		}
		for _, x := range sess {
			if containsStr(est, x.Peer) {
				summ = append(summ, fmt.Sprintf("%-10s %-28s %s", x.Peer, pn(x.Peer), pyLower(x.State)))
			}
		}
		summ = append(summ, fmt.Sprintf("set aside (not counted): %d session(s)", len(aside)))
		return ok, note, strings.Join(summ, "\n") + "\n\x00RAW\n" + strings.Join(texts, "\n"), nil
	case "mnha":
		cmds := CMDS["mnha"][os_]
		if len(cmds) == 0 {
			return false, "", "", unavailable("%s", "no HA check for "+os_)
		}
		devs := pyjson.StrList(c.V("devices"))
		idx := -1
		for i, x := range devs {
			if x == n {
				idx = i
				break
			}
		}
		if idx < 0 {
			return false, "", "", fmt.Errorf("%s is not in list", pyStrRepr(n))
		}
		first := idx == 0
		if os_ == "junos" && !mnhaRE.MatchString(e.Devs[n].Str("role")) {
			t, err := e.Read(n, []string{"show chassis cluster status"})
			if err != nil {
				return false, "", "", err
			}
			ok := primaryRE.MatchString(t[0]) && secondaryRE.MatchString(t[0])
			w := "not formed"
			if ok {
				w = "primary and secondary up"
			}
			return ok, "chassis cluster " + w, t[0], nil
		}
		t, err := e.Read(n, cmds)
		if err != nil {
			return false, "", "", err
		}
		var ok bool
		if os_ == "ftd" {
			ok = ftdActRE[first].MatchString(t[0])
		} else {
			ok = srgRE[first].MatchString(t[0])
		}
		w := "backup"
		if first {
			w = "active"
		}
		if ok {
			w += " as expected"
		} else {
			w += " NOT confirmed"
		}
		return ok, w, t[0], nil
	case "roce":
		cmds := CMDS["roce"][os_]
		if len(cmds) == 0 {
			return false, "", "", unavailable("%s", "no RoCE check for "+os_)
		}
		tt, err := e.Read(n, cmds)
		if err != nil {
			return false, "", "", err
		}
		t := tt[0]
		var ok bool
		switch os_ {
		case "cumulus":
			ok = roceCumRE.MatchString(t)
		case "junos":
			ok = roceJPfcRE.MatchString(t) && roceJEcnRE.MatchString(t)
		case "eos", "nxos":
			ok = roceOnRE.MatchString(t) && !roceOffRE.MatchString(t)
		default:
			ok = strings.Contains(pyLower(t), "pfc")
		}
		w := "not found"
		if ok {
			w = "in effect"
		}
		return ok, "PFC/ECN " + w, t, nil
	case "lossless":
		cmd := BackupCmd[os_]
		pl, havePl := e.plannedText(n)
		if cmd == "" || !havePl {
			o := os_
			if o == "" {
				o = "this device"
			}
			return false, "", "", unavailable("%s", "no running-configuration reader or planned configuration for "+o)
		}
		run, err := e.Read(n, []string{cmd})
		if err != nil {
			return false, "", "", err
		}
		planned := StrSet{}
		for x := range CfgNorm(pl, os_) {
			if losslessRE.MatchString(x) {
				planned.add(x)
			}
		}
		e.Mu.Lock()
		P := e.tuningSwap(n, planned, os_)
		e.Mu.Unlock()
		if len(P) == 0 {
			return true, "no lossless lines planned on this device", "", nil
		}
		have := CfgNorm(run[0], os_)
		var miss []string
		for _, x := range P.Sorted() {
			if _, ok := have[x]; !ok {
				miss = append(miss, x)
			}
		}
		note := fmt.Sprintf("%d of %d planned lossless lines in effect", len(P)-len(miss), len(P))
		if len(miss) > 0 {
			sh := miss
			if len(sh) > 3 {
				sh = sh[:3]
			}
			note += "; missing: " + strings.Join(sh, "; ")
			if len(miss) > 3 {
				note += fmt.Sprintf(" +%d more", len(miss)-3)
			}
		}
		var tl []string
		for i, x := range miss {
			if i >= 300 {
				break
			}
			tl = append(tl, "- "+x)
		}
		return len(miss) == 0, note, strings.Join(tl, "\n"), nil
	case "software":
		rel := e.Devs[n].V("release")
		var relM *pyjson.Map
		if pyjson.Truthy(rel) {
			m, ok := rel.(*pyjson.Map)
			if !ok {
				return false, "", "", fmt.Errorf("'%s' object has no attribute 'get'", pyTypeName(rel))
			}
			relM = m
		}
		tgt := ""
		if relM != nil && relM.Truthy("target") {
			tgt = pyStripU(PyStr(relM.V("target")))
		}
		if tgt == "" {
			return true, "no target release in the profile", "", nil
		}
		cmd := VersionCmd[os_]
		if cmd == "" {
			return false, "", "", unavailable("%s", "no version check for "+os_)
		}
		tt, err := e.Read(n, []string{cmd})
		if err != nil {
			return false, "", "", err
		}
		t := tt[0]
		ok := strings.Contains(pyLower(t), pyLower(tgt))
		if !ok {
			vt := vnum(tgt)
			for _, x := range verTokRE.FindAllString(t, -1) {
				if vnum(x) == vt {
					ok = true
					break
				}
			}
		}
		if ok {
			return true, "at " + tgt, t, nil
		}
		got := "?"
		if m := distribRE.FindStringSubmatch(t); m != nil {
			got = m[1]
		} else if m := verWordRE.FindStringSubmatch(t); m != nil {
			got = m[1]
		}
		return false, fmt.Sprintf("running %s, target %s", got, tgt), t, nil
	case "cabling":
		exp := e.expectedLinks(n)
		if len(exp.keys) == 0 {
			return true, "no planned links on this device", "", nil
		}
		cmd := LLDPCmd[os_]
		if cmd == "" {
			return false, "", "", unavailable("%s", "no LLDP check for "+os_)
		}
		tt, err := e.Read(n, []string{cmd})
		if err != nil {
			return false, "", "", err
		}
		t := tt[0]
		seen, _, understood := LLDPPairs(t)
		if !understood {
			return false, "", "", unavailable("%s", "the LLDP output was not understood")
		}
		lps := append([]string(nil), exp.keys...)
		sort.Strings(lps)
		var bad []string
		okN, skip := 0, 0
		e.Mu.Lock()
		for _, lp := range lps {
			pd, pp := exp.m[lp][0], exp.m[lp][1]
			pn := e.peerName(pd)
			ps := e.St[pn]
			got, have := seen[lp]
			if !have {
				got, have = seen[NormPort(lp)]
			}
			if have && pyLower(strings.SplitN(got.Name, ".", 2)[0]) == "cumulus" {
				have = false
			}
			psFlag, psStage := "", int64(0)
			if ps != nil {
				psFlag, psStage = ps.Str("flag"), ps.Int("stage")
			}
			if psFlag == "quarantined" || (!have && psStage < 4) {
				skip++
				continue
			}
			if !have {
				bad = append(bad, fmt.Sprintf("%s: expected %s %s, no neighbor", lp, e.devShort(pn, pd), pp))
			} else if !sameDev(got.Name, pn, e.devShort(pn, "")) || NormPort(got.Port) != NormPort(pp) {
				bad = append(bad, fmt.Sprintf("%s: expected %s %s, saw %s %s", lp, e.devShort(pn, pd), pp, got.Name, got.Port))
			} else {
				okN++
			}
		}
		e.Mu.Unlock()
		note := fmt.Sprintf("%d link(s) as planned", okN)
		if skip > 0 {
			note += fmt.Sprintf(" (%d not checked: far end set aside or not configured)", skip)
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
			note = fmt.Sprintf("%d miswired or missing: %s; %s", len(bad), strings.Join(sh, "; ")+more, note)
		}
		nl := ""
		if len(bad) > 0 {
			nl = "\n"
		}
		return len(bad) == 0, note, strings.Join(bad, "\n") + nl + "\x00RAW\n" + t, nil
	}
	return false, "", "", unavailable("%s", "unknown check "+kind)
}

// pyIntAny is int(x) for a JSON value.
func pyIntAny(v any) int64 {
	switch x := v.(type) {
	case string:
		i, _ := pyInt(x)
		return i
	}
	return pyjson.Int(v)
}

// ------------------------------------------------------------------------------------- one check over its devices

func (e *Estate) autoGet(k, c string) *pyjson.Map { return e.AutoRes.M(k + "|" + c) }

func (e *Estate) autoSet(k, c string, v *pyjson.Map) {
	e.AutoRes.Set(k+"|"+c, v)
	e.KVSet("auto_res", e.AutoRes)
}

func (e *Estate) autoPopKind(kinds ...string) {
	for _, b := range e.Builds {
		for _, cv := range b.L("gate") {
			c := cv.(*pyjson.Map)
			if containsStr(kinds, PyStr(c.V("kind"))) {
				e.AutoRes.Delete(b.Str("key") + "|" + c.Str("key"))
			}
		}
	}
}

func (e *Estate) readerWorkers() int {
	w := int64(16)
	if v, ok := e.Conf.Get("reader_workers"); ok {
		w = pyIntAny(v)
	}
	return int(w)
}

// poolMap runs f over the items on min(reader_workers, n) workers; results in input order.
func (e *Estate) poolMap(n int, f func(i int)) {
	w := e.readerWorkers()
	if w > n {
		w = n
	}
	if w < 1 {
		w = 1
	}
	if w == 1 {
		for i := 0; i < n; i++ {
			f(i)
		}
		return
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for k := 0; k < w; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				f(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}

func withTS(res *pyjson.Map, ts float64) *pyjson.Map {
	r := res.Copy()
	r.Set("ts", ts)
	return r
}

// RunAuto runs one automatic gate check and stores its result.
func (e *Estate) RunAuto(k string, c *pyjson.Map) {
	key := c.Str("key")
	kind := c.Str("kind")
	e.Mu.Lock()
	prev := e.autoGet(k, key)
	switch kind {
	case "nccl":
		var res *pyjson.Map
		r := e.Results.M("nccl")
		if !pyjson.Truthy(r) {
			res = pyjson.O("result", "waiting", "note", "waiting for the NCCL result (runner or console entry)", "evidence", pyjson.NewMap())
		} else {
			ok := pyFloatAnyZ(r.V("busbw")) >= pyFloatAnyZ(r.V("target"))
			rs := "blocked"
			if ok {
				rs = "pass"
			}
			by := ""
			if r.Has("by") {
				by = PyStr(r.V("by"))
			}
			txt := ""
			if r.Has("text") {
				txt = PyStr(r.V("text"))
			}
			res = pyjson.O("result", rs, "note", fmt.Sprintf("bus bandwidth %s GB/s against %s GB/s (%s)", PyStr(r.V("busbw")), PyStr(r.V("target")), by),
				"evidence", pyjson.O("nccl", pyjson.O("ok", ok, "note", "", "text", pyTail(txt, 1500))))
		}
		e.autoSet(k, key, withTS(res, Now()))
		e.Mu.Unlock()
		return
	case "accept", "tuned", "isolation", "inference", "failover", "soak", "alerts": // HS v2.04: result-backed checks
		res := e.gateKindEval(kind, pyjson.StrList(c.V("planes")))
		e.autoSet(k, key, withTS(res, Now()))
		e.Mu.Unlock()
		return
	}
	ev := pyjson.NewMap()
	var bad []string
	why := ""
	all := pyjson.StrList(c.V("devices"))
	var devs, todo []string
	for _, n := range all {
		if e.flag(n) != "quarantined" {
			devs = append(devs, n)
		}
	}
	aside := len(all) - len(devs)
	for _, n := range devs {
		if e.stage(n) < 5 && !e.Devs[n].Truthy("aggregate") {
			bad = append(bad, n)
			ev.Set(n, pyjson.O("ok", false, "note", "not yet verified", "text", ""))
			continue
		}
		todo = append(todo, n)
	}
	e.Mu.Unlock()
	type res3 struct {
		ok         bool
		note, text string
		err        string
		unavail    bool
	}
	out := make([]res3, len(todo))
	e.poolMap(len(todo), func(i int) {
		defer func() {
			if r := recover(); r != nil {
				out[i] = res3{false, fmt.Sprintf("read failed: %v", r), "", "", false}
			}
		}()
		ok, note, text, err := e.EvalKind(kind, todo[i], c)
		if err != nil {
			if ru, isU := isUnavailable(err); isU {
				out[i] = res3{err: ru.Msg, unavail: true}
				return
			}
			out[i] = res3{false, "read failed: " + err.Error(), "", "", false}
			return
		}
		out[i] = res3{ok, note, text, "", false}
	})
	for i, n := range todo {
		r := out[i]
		if r.unavail {
			if why == "" {
				why = r.err
			}
			continue
		}
		t1, raw := r.text, ""
		if j := strings.Index(r.text, "\x00RAW\n"); j >= 0 {
			t1, raw = r.text[:j], r.text[j+5:]
		}
		txt := pyTail(r.text, 1500)
		if raw != "" {
			txt = pyTail(t1, 4000)
		}
		ev.Set(n, pyjson.O("ok", r.ok, "note", r.note, "text", txt, "raw", pyTail(raw, 2500)))
		if !r.ok {
			bad = append(bad, n)
		}
	}
	t := Now()
	var res *pyjson.Map
	asideS := ""
	if aside > 0 {
		asideS = fmt.Sprintf(" (%d set aside)", aside)
	}
	e.Mu.Lock()
	defer e.Mu.Unlock()
	switch {
	case why != "":
		res = pyjson.O("result", "unavailable", "note", fmt.Sprintf("automatic check unavailable (%s): confirm by hand", why), "evidence", ev)
	case len(devs) == 0:
		res = pyjson.O("result", "pass", "note", fmt.Sprintf("no device to read: all %d set aside", aside), "evidence", ev)
	case len(bad) == 0:
		res = pyjson.O("result", "pass", "note", fmt.Sprintf("%d of %d devices pass%s", len(devs), len(devs), asideS), "evidence", ev)
	default:
		first := t
		if prev != nil && pyjson.Truthy(prev.V("first_fail")) {
			first = pyjson.Float(prev.V("first_fail"))
		}
		grace := 300.0
		if v, ok := c.Get("grace_s"); ok {
			grace = pyFloatAnyZ(v)
		}
		late := t-first > grace
		rs := "running"
		if late {
			rs = "blocked"
			if c.Truthy("warn_only") {
				rs = "warn"
			}
		}
		var parts []string
		for i, n := range bad {
			if i >= 3 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s: %s", e.short(n), PyStr(ev.M(n).V("note"))))
		}
		var ff any = first
		if prev != nil && pyjson.Truthy(prev.V("first_fail")) {
			ff = prev.V("first_fail")
		}
		res = pyjson.O("result", rs, "note", fmt.Sprintf("%d of %d pass%s; %s", len(devs)-len(bad), len(devs), asideS, strings.Join(parts, "; ")),
			"evidence", ev, "first_fail", ff)
	}
	var prevRes any
	if prev != nil {
		prevRes = prev.V("result")
	}
	changed := !(prevRes != nil && PyStr(prevRes) == res.Str("result") && isStr(prevRes))
	e.autoSet(k, key, withTS(res, t))
	if rs := res.Str("result"); changed && (rs == "pass" || rs == "blocked" || rs == "warn") {
		lvl := "fail"
		if rs == "pass" {
			lvl = "info"
		} else if rs == "warn" {
			lvl = "warn"
		}
		e.ev(fmt.Sprintf("%s gate: '%s' %s -- %s", e.BuildByKey[k].Str("name"), c.Str("label"), rs, pyHead(res.Str("note"), 160)), lvl, "")
	}
}

func isStr(v any) bool { _, ok := v.(string); return ok }

// pyFloatAnyZ is float(x) of a stored number or numeric string (0 when it is neither).
func pyFloatAnyZ(v any) float64 {
	f, _ := pyFloatAny(v)
	return f
}

// pyFloatAny is float(x): numbers, bools and numeric strings.
func pyFloatAny(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		return pyFloat(x)
	}
	return 0, false
}

// ------------------------------------------------------------------------------------- the checker

func (e *Estate) checkEvery() float64 {
	if v, ok := e.Conf.Get("check_every_s"); ok {
		return pyFloatAnyZ(v)
	}
	return 60
}

// checkerPass runs the due automatic checks of every build at its gate (Python: checker, one pass).
func (e *Estate) checkerPass() {
	type item struct {
		k string
		c *pyjson.Map
	}
	var todo []item
	e.Mu.Lock()
	for _, b := range e.Builds {
		bs := e.bstate(b.Str("key"))
		if st := bs.Str("state"); st != "gate" && st != "blocked" {
			continue
		}
		for _, cv := range b.L("gate") {
			c := cv.(*pyjson.Map)
			if c.Str("type") != "auto" || e.GateRes[[2]string{b.Str("key"), c.Str("key")}] != nil {
				continue
			}
			r := e.autoGet(b.Str("key"), c.Str("key"))
			if r != nil && (r.Str("result") == "pass" || Now()-pyjson.Float(r.V("ts")) < e.checkEvery()) {
				continue
			}
			todo = append(todo, item{b.Str("key"), c})
		}
	}
	e.Mu.Unlock()
	// HS v1.56 r3 (DECISIONS P5-2): a read session opened in this pass serves the device's next read in the pass
	e.gate().beginPass()
	defer e.gate().endPass()
	for _, x := range todo {
		e.RunAuto(x.k, x.c)
	}
	e.rmaAutomation()
	e.bondSteer()
}

// Checker is the automatic-check loop (every 10 s; a standby host checks nothing).
func (e *Estate) Checker() {
	for {
		if e.roleNow() != "primary" {
			time.Sleep(10 * time.Second)
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "checker: %v\n", r)
				}
			}()
			e.checkerPass()
		}()
		time.Sleep(10 * time.Second)
	}
}

func (e *Estate) roleNow() string {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	return e.Role
}

// GateRecheck runs one pass of a build's automatic checks now (waits up to 60 s for the result).
func (e *Estate) GateRecheck(k, user string) (bool, string) {
	e.Mu.Lock()
	b := e.BuildByKey[k]
	if b == nil {
		e.Mu.Unlock()
		return false, "unknown build"
	}
	var checks []*pyjson.Map
	for _, cv := range b.L("gate") {
		if c := cv.(*pyjson.Map); c.Str("type") == "auto" {
			checks = append(checks, c)
		}
	}
	e.Mu.Unlock()
	if len(checks) == 0 {
		return false, "this gate has no automatic checks"
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.gate().beginPass() // P5-2: one pass
		defer e.gate().endPass()
		for _, c := range checks {
			func() {
				defer func() {
					if r := recover(); r != nil {
						fmt.Fprintf(os.Stderr, "recheck: %v\n", r)
					}
				}()
				e.RunAuto(k, c)
			}()
		}
	}()
	e.Audit(user, "gate-recheck", k, "")
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		return true, fmt.Sprintf("re-checking %d automatic check(s): still reading, results appear in the gate", len(checks))
	}
	e.Mu.Lock()
	defer e.Mu.Unlock()
	var res []string
	for _, c := range checks {
		r := "?"
		if a := e.autoGet(k, c.Str("key")); a != nil && a.Has("result") {
			r = PyStr(a.V("result"))
		}
		res = append(res, r)
	}
	uniq := map[string]int{}
	for _, x := range res {
		uniq[x]++
	}
	keys := make([]string, 0, len(uniq))
	for x := range uniq {
		keys = append(keys, x)
	}
	sort.Strings(keys)
	var parts []string
	for _, x := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", uniq[x], x))
	}
	return true, fmt.Sprintf("re-checked %d automatic check(s): %s", len(checks), strings.Join(parts, ", "))
}

// ------------------------------------------------------------------------------------- bond steering (v498)

func (e *Estate) bondSteer() {
	base := "/sys/class/net/bond0/bonding"
	b, err := os.ReadFile(base + "/slaves")
	if err != nil {
		return
	}
	sl := strings.Fields(string(b))
	if len(sl) != 2 {
		return
	}
	e.Mu.Lock()
	defer e.Mu.Unlock()
	if e.bondHome == "" {
		home := ""
		if np, err := os.ReadFile("/etc/netplan/60-estate-bond.yaml"); err == nil {
			if m := regexp.MustCompile(`primary:\s*([A-Za-z0-9_.-]+)`).FindStringSubmatch(string(np)); m != nil {
				home = m[1]
			}
		}
		if containsStr(sl, home) {
			e.bondHome = home
		} else {
			e.bondHome = sl[0]
		}
	}
	a := e.bondHome
	other := sl[0]
	if other == a {
		other = sl[1]
	}
	var cores []string
	for _, n := range e.DevOrder {
		if e.Devs[n].Truthy("seed_ip") {
			cores = append(cores, n)
		}
	}
	sort.Strings(cores)
	want := a
	if len(cores) == 2 && (e.waveOpen(cores[0]) || e.waveOpen(cores[1])) {
		if e.stage(cores[0]) >= 4 && e.stage(cores[1]) < 4 {
			want = other
		}
	}
	cur, err := os.ReadFile(base + "/active_slave")
	if err != nil {
		return
	}
	if strings.TrimSpace(string(cur)) == want {
		return
	}
	for _, f := range []string{"primary", "active_slave"} {
		if err := os.WriteFile(base+"/"+f, []byte(want), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "bond_steer: %v\n", err)
			return
		}
	}
	why := "home NIC"
	if want == other {
		why = fmt.Sprintf("toward %s, which has not staged its configuration", e.short(cores[1]))
	}
	e.ev(fmt.Sprintf("provisioning host: bond0 now listens on %s (%s)", want, why), "info", "")
}

// PyFloatAny is float(x) of a JSON value (numbers, bools, numeric strings).
func PyFloatAny(v any) (float64, bool) { return pyFloatAny(v) }
