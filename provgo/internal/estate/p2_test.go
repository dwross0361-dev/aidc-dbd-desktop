package estate

// Phase 2 unit tests: every parser on every per-OS fixture, every check kind on every fixture device, and the counter,
// acceptance, isolation, tuning and difference-view scenarios, each compared with the Python service's own result.
//
// Expected values come from testdata/p2/expected.json, written by genexp.py from estate_build.py. With ESTATE_PY set to
// an estate_build.py, the test runs genexp.py first and compares with what Python says now.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"aidc-dbd/provgo/internal/pyjson"
)

const p2dir = "testdata/p2"

func generic(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("json: %v: %.200s", err, s)
	}
	return v
}

func gj(t *testing.T, v any) any {
	t.Helper()
	switch x := v.(type) {
	case *pyjson.Map, []any:
		return generic(t, pyjson.Dumps(x))
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return generic(t, string(b))
}

func loadExpected(t *testing.T) map[string]any {
	t.Helper()
	path := filepath.Join(p2dir, "expected.json")
	if py := os.Getenv("ESTATE_PY"); py != "" {
		path = filepath.Join(t.TempDir(), "expected.json")
		cmd := exec.Command("python3", "-I", filepath.Join(p2dir, "genexp.py"), py, filepath.Join(p2dir, "fixtures"), path)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("genexp: %v\n%s", err, out)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return generic(t, string(b)).(map[string]any)
}

func loadCases(t *testing.T) map[string]any {
	b, err := os.ReadFile(filepath.Join(p2dir, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	return generic(t, string(b)).(map[string]any)
}

type diffCounter struct {
	t    *testing.T
	n, f int
}

func (d *diffCounter) same(what string, want, got any) {
	d.t.Helper()
	d.n++
	if !reflect.DeepEqual(want, got) {
		d.f++
		wb, _ := json.Marshal(want)
		gb, _ := json.Marshal(got)
		d.t.Errorf("%s:\n  python: %.600s\n  go:     %.600s", what, wb, gb)
	}
}

func pairsOf(f *FlatNum) [][2]any {
	out := [][2]any{}
	for _, k := range f.Keys {
		out = append(out, [2]any{k, f.M[k]})
	}
	return out
}

func countsOf(c *AccCount) map[string]any {
	seen := StrSet{}
	for s := range c.Seen {
		seen.add(s)
	}
	return map[string]any{"pfc_tx": c.PfcTx, "pfc_rx": c.PfcRx, "ecn": c.Ecn, "drops": c.Drops, "wd": c.Wd, "seen": seen.Sorted()}
}

func lldpOf(t string) any {
	p, keys, ok := LLDPPairs(t)
	if !ok {
		return nil
	}
	out := [][2]any{}
	for _, k := range keys {
		out = append(out, [2]any{k, []string{p[k].Name, p[k].Port}})
	}
	return out
}

func sessOf(s []BGPSess) [][2]string {
	out := [][2]string{}
	for _, x := range s {
		out = append(out, [2]string{x.Peer, x.State})
	}
	return out
}

func TestP2Parsers(t *testing.T) {
	exp := loadExpected(t)["parsers"].(map[string]any)
	d := &diffCounter{t: t}
	for key, want := range exp {
		text, err := readTextPy(filepath.Join(p2dir, "fixtures", "sim", key))
		if err != nil {
			t.Fatal(err)
		}
		w := want.(map[string]any)
		est, tot, bad := ParseBGP([]string{text})
		got := map[string]any{
			"bgp_sessions":     sessOf(BGPSessions([]string{text})),
			"text_bgp":         sessOf(textBGP(text)),
			"parse_bgp":        []any{est, tot, bad},
			"lldp_pairs":       lldpOf(text),
			"acc_flat_text":    pairsOf(AccFlatText(text)),
			"acc_counts":       countsOf(AccCounts(AccFlatText(text))),
			"cfg_norm_cumulus": CfgNorm(text, "cumulus").Sorted(),
			"cfg_norm_other":   CfgNorm(text, "junos").Sorted(),
			"vnum":             vnum(pyHead(text, 200)),
		}
		for k := range w {
			d.same(key+" "+k, w[k], gj(t, got[k]))
		}
	}
	t.Logf("parsers: %d comparisons over %d fixture outputs, %d mismatches", d.n, len(exp), d.f)
}

func TestP2ParserCases(t *testing.T) {
	exp := loadExpected(t)["scenarios"].(map[string]any)
	P := loadCases(t)["parsers"].(map[string]any)
	d := &diffCounter{t: t}
	for i, x := range P["nv_expand"].([]any) {
		d.same(fmt.Sprintf("nv_expand %d", i), exp["nv_expand"].([]any)[i], gj(t, nvExpand(x.(string))))
	}
	for i, x := range P["cfg_norm"].([]any) {
		a := x.([]any)
		d.same(fmt.Sprintf("cfg_norm %d", i), exp["cfg_norm"].([]any)[i], gj(t, CfgNorm(a[0].(string), a[1].(string)).Sorted()))
	}
	for i, x := range P["same_dev"].([]any) {
		a := x.([]any)
		s := func(v any) string { str, _ := v.(string); return str }
		d.same(fmt.Sprintf("same_dev %d", i), exp["same_dev"].([]any)[i], sameDev(s(a[0]), s(a[1]), s(a[2])))
	}
	for i, x := range P["vnum"].([]any) {
		d.same(fmt.Sprintf("vnum %d", i), exp["vnum"].([]any)[i], vnum(x.(string)))
	}
	for i, x := range P["lldp_text"].([]any) {
		d.same(fmt.Sprintf("lldp_text %d", i), exp["lldp_text"].([]any)[i], gj(t, lldpOf(x.(string))))
	}
	for i, x := range P["bgp_texts"].([]any) {
		var texts []string
		for _, s := range x.([]any) {
			texts = append(texts, s.(string))
		}
		d.same(fmt.Sprintf("bgp_text %d", i), exp["bgp_text"].([]any)[i], gj(t, sessOf(BGPSessions(texts))))
	}
	for i, x := range P["acc_flat_text"].([]any) {
		d.same(fmt.Sprintf("acc_flat_text %d", i), exp["acc_flat_text"].([]any)[i], gj(t, pairsOf(AccFlatText(x.(string)))))
	}
	for i, x := range P["acc_parse"].([]any) {
		a := x.([]any)
		kv, nic := AccParse(a[0].(string), a[1].(string))
		nm := pyjson.NewMap()
		for _, k := range nic.Keys {
			nm.Set(k, nic.M[k])
		}
		d.same(fmt.Sprintf("acc_parse %d", i), exp["acc_parse"].([]any)[i], gj(t, []any{kv, nm}))
	}
	for i, x := range P["slug"].([]any) {
		d.same(fmt.Sprintf("slug %d", i), exp["slug"].([]any)[i], Slug(x.(string)))
	}
	t.Logf("parser cases: %d comparisons, %d mismatches", d.n, d.f)
}

// p2Estate builds an estate on the fixture plan, as genexp.py does.
func p2Estate(t *testing.T) (*Estate, string) {
	t.Helper()
	W := t.TempDir()
	fx, _ := filepath.Abs(filepath.Join(p2dir, "fixtures"))
	pb, err := os.ReadFile(filepath.Join(fx, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(W, "plan.json"), pb, 0o644))
	must(os.WriteFile(filepath.Join(W, "kea.json"), []byte(`{"Dhcp4": {"subnet4": [{"id": 1, "subnet": "127.0.0.0/8"}]}}`), 0o644))
	must(os.WriteFile(filepath.Join(W, "secrets.env"), []byte("TARGET_BUSBW=380\n"), 0o644))
	conf := pyjson.O("state_dir", filepath.Join(W, "st"), "secrets", filepath.Join(W, "secrets.env"), "web_root", filepath.Join(W, "www"),
		"bundle_dir", filepath.Join(fx, "bundle"), "reader_sim_dir", filepath.Join(W, "sim"))
	must(os.WriteFile(filepath.Join(W, "conf.json"), []byte(pyjson.Dumps(conf)), 0o644))
	must(exec.Command("cp", "-r", filepath.Join(fx, "sim"), filepath.Join(W, "sim")).Run())
	e, err := New(filepath.Join(W, "plan.json"), filepath.Join(W, "conf.json"), filepath.Join(W, "kea.json"))
	must(err)
	e.Out = nil
	plan, _ := pyjson.LoadsMap(pb)
	for _, dv := range plan.L("devices") {
		dm := dv.(*pyjson.Map)
		st := dm.M("_state")
		for _, k := range st.Keys() {
			e.St[dm.Str("name")].Set(k, st.V(k))
		}
	}
	return e, W
}

func evOut(e *Estate, kind, n string, c *pyjson.Map) map[string]any {
	ok, note, text, err := func() (ok bool, note, text string, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%v", r)
			}
		}()
		return e.EvalKind(kind, n, c)
	}()
	if err != nil {
		if ru, isU := isUnavailable(err); isU {
			return map[string]any{"unavailable": ru.Msg}
		}
		return map[string]any{"exc": err.Error()}
	}
	return map[string]any{"ok": ok, "note": note, "text": text}
}

func TestP2Checks(t *testing.T) {
	exp := loadExpected(t)["checks"].([]any)
	cases := loadCases(t)["checks"].([]any)
	e, W := p2Estate(t)
	d := &diffCounter{t: t}
	kinds := map[string]int{}
	oses := map[string]bool{}
	for i, s := range cases {
		st := s.(map[string]any)
		if wr, ok := st["write"].(map[string]any); ok {
			for rel, text := range wr {
				p := filepath.Join(W, "sim", rel)
				_ = os.MkdirAll(filepath.Dir(p), 0o755)
				if err := os.WriteFile(p, []byte(text.(string)), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			continue
		}
		kind, n := st["kind"].(string), st["dev"].(string)
		c := pyjson.NewMap()
		if cm, ok := st["c"].(map[string]any); ok {
			b, _ := json.Marshal(cm)
			c, _ = pyjson.LoadsMap(b)
		}
		if kind == "mnha" && !c.Has("devices") {
			c.Set("devices", []any{n})
		}
		got := evOut(e, kind, n, c)
		d.same(fmt.Sprintf("step %d %s %s", i, kind, n), exp[i], gj(t, got))
		kinds[kind]++
		oses[e.Devs[n].Str("os")] = true
	}
	t.Logf("checks: %d comparisons (kinds %v; platforms %d), %d mismatches", d.n, kinds, len(oses), d.f)
}

func snapOf(t *testing.T, v any) *AccSnap {
	if v == nil {
		return nil
	}
	s := &AccSnap{m: map[string]accSnapEntry{}}
	m := v.(map[string]any)
	// Python dict order = the JSON object order; cases.json keeps the writer's order, so read the keys in order
	b, _ := json.Marshal(m)
	om, _ := pyjson.LoadsMap(b)
	_ = om
	for _, n := range sortedKeysByCase(m) {
		x := m[n]
		s.keys = append(s.keys, n)
		if str, ok := x.(string); ok {
			s.m[n] = accSnapEntry{nil, str}
			continue
		}
		cm := x.(map[string]any)
		c := newAccCount()
		get := func(k string) float64 { f, _ := cm[k].(float64); return f }
		c.PfcTx, c.PfcRx, c.Ecn, c.Drops, c.Wd = get("pfc_tx"), get("pfc_rx"), get("ecn"), get("drops"), get("wd")
		for _, sv := range cm["seen"].([]any) {
			c.Seen[sv.(string)] = true
		}
		s.m[n] = accSnapEntry{c, ""}
	}
	return s
}

// sortedKeysByCase returns snapshot keys in the order mkcases.py wrote them (device names sort the same way there).
func sortedKeysByCase(m map[string]any) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	for i := 0; i < len(ks); i++ {
		for j := i + 1; j < len(ks); j++ {
			if ks[j] < ks[i] {
				ks[i], ks[j] = ks[j], ks[i]
			}
		}
	}
	return ks
}

func toMap(t *testing.T, v any) *pyjson.Map {
	if v == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	m, err := pyjson.LoadsMap(b)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestP2Scenarios(t *testing.T) {
	exp := loadExpected(t)["scenarios"].(map[string]any)
	C := loadCases(t)
	e, _ := p2Estate(t)
	d := &diffCounter{t: t}
	// counters
	for _, nv := range C["counters"].([]any) {
		n := nv.(string)
		c, err := e.accCountsOf(n)
		var got any
		if err != nil {
			got = map[string]any{"err": err.Error()}
		} else {
			got = countsOf(c)
		}
		d.same("acc_counts_of "+n, exp["acc_counts_of"].(map[string]any)[n], gj(t, got))
	}
	// acceptance scoring
	for i, sv := range C["acc_score"].([]any) {
		sc := sv.(map[string]any)
		e.Conf.Delete("TARGET_BUSBW")
		if v, ok := sc["conf_target"]; ok && v != nil {
			e.Conf.Set("TARGET_BUSBW", int64(v.(float64)))
		}
		crit := toMap(t, sc["crit"])
		if crit == nil {
			crit = pyjson.NewMap()
		}
		rc := 0
		if v, ok := sc["rc"]; ok {
			rc = int(v.(float64))
		}
		r := e.accScore(sc["test"].(string), sc["text"].(string), snapOf(t, sc["s0"]), snapOf(t, sc["s1"]), crit, rc)
		d.same(fmt.Sprintf("acc_score %d", i), exp["acc_score"].([]any)[i], gj(t, r))
	}
	e.Conf.Delete("TARGET_BUSBW")
	// results
	for i, sv := range C["results"].([]any) {
		sc := sv.(map[string]any)
		e.Results = pyjson.NewMap()
		var got map[string]any
		switch sc["kind"] {
		case "accept":
			ok, msg := e.SubmitAcceptText(sc["text"].(string), "op1", toMap(t, sc["crit"]))
			var stored any
			if a := e.Results.M("accept"); a != nil {
				stored = gj(t, a.V("tests"))
			}
			got = map[string]any{"submit": []any{ok, msg}, "stored": stored, "eval": gj(t, e.acceptEval())}
		case "isolation":
			if ts, ok := sc["tenants"].([]any); ok {
				var tl []any
				for _, x := range ts {
					tl = append(tl, pyjson.O("tenant", x))
				}
				e.Plan.Set("isolation", pyjson.O("tenants", tl))
			} else {
				e.Plan.Delete("isolation")
			}
			ok, msg := e.SubmitIsolationText(sc["text"].(string), "op1")
			var stored any
			if a := e.Results.M("isolation"); a != nil {
				stored = gj(t, a.V("tests"))
			}
			got = map[string]any{"submit": []any{ok, msg}, "stored": stored, "eval": gj(t, e.isolationEval())}
		case "inference": // HS v1.98
			if ts, ok := sc["tenants"].([]any); ok {
				var tl []any
				for _, x := range ts {
					p := x.([]any)
					tl = append(tl, pyjson.O("tenant", p[0], "rps", int64(10), "p99_ms", int64(p[1].(float64))))
				}
				e.Plan.Set("inference", pyjson.O("tool", "hey", "tenants", tl))
			} else {
				e.Plan.Delete("inference")
			}
			ok, msg := e.SubmitInferenceText(sc["text"].(string), "op1")
			var stored any
			if a := e.Results.M("inference"); a != nil {
				stored = gj(t, a.V("tests"))
			}
			got = map[string]any{"submit": []any{ok, msg}, "stored": stored, "eval": gj(t, e.inferenceEval())}
		}
		d.same(fmt.Sprintf("results %d", i), exp["results"].([]any)[i], gj(t, got))
	}
	// tuning
	planTuning := e.Plan.V("tuning")
	for i, sv := range C["tuned"].([]any) {
		sc := sv.(map[string]any)
		e.KVSet("tuning", toMap(t, sc["state"]))
		if p, ok := sc["plan"]; ok {
			e.Plan.Set("tuning", toMap(t, p))
		} else {
			e.Plan.Set("tuning", planTuning)
		}
		d.same(fmt.Sprintf("tuned %d", i), exp["tuned"].([]any)[i], gj(t, e.tunedEval()))
	}
	e.Plan.Set("tuning", planTuning)
	k := 0
	for i, sv := range C["swap"].([]any) {
		sc := sv.(map[string]any)
		e.KVSet("tuning", toMap(t, sc["state"]))
		pl := StrSet{}
		for _, x := range sc["planned"].([]any) {
			pl.add(x.(string))
		}
		d.same(fmt.Sprintf("swap %d", i), exp["swap"].([]any)[k], gj(t, e.tuningSwap(sc["dev"].(string), pl, "cumulus").Sorted()))
		d.same(fmt.Sprintf("swap %d lossless", i), exp["swap"].([]any)[k+1], gj(t, evOut(e, "lossless", sc["dev"].(string), pyjson.NewMap())))
		k += 2
	}
	e.KVSet("tuning", pyjson.NewMap())
	// difference view
	for i, sv := range C["diff"].([]any) {
		sc := sv.(map[string]any)
		n := sc["dev"].(string)
		dir := e.bdir(n)
		ents, _ := os.ReadDir(dir)
		for _, f := range ents {
			os.Remove(filepath.Join(dir, f.Name()))
		}
		for _, bv := range sc["backups"].([]any) {
			b := bv.([]any)
			if err := os.WriteFile(filepath.Join(dir, b[0].(string)), []byte(b[1].(string)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got := []any{gj(t, e.Diff(n, "")), gj(t, e.Diff(n, "prev"))}
		d.same(fmt.Sprintf("diff %d %s", i, n), exp["diff"].([]any)[i], got)
	}
	// reports (HS v2.02), phase by phase: "generated" masked; the certificate rests on a post report with it fixed
	raw, err := os.ReadFile(filepath.Join(p2dir, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	all, err := pyjson.LoadsMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	RC := all.M("reports")
	e.Plan.M("site").Update(RC.M("site"))
	e.Plan.Set("standards", RC.V("standards"))
	e.Plan.Set("isolation", pyjson.O("tenants", RC.V("isolation")))
	e.BuildByKey["be"].Set("gate", RC.V("gate"))
	e.Results = pyjson.DeepCopy(RC.M("results")).(*pyjson.Map)
	var mask func(v any) any
	mask = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, y := range x {
				if k == "generated" {
					out[k] = "<T>"
				} else {
					out[k] = mask(y)
				}
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i := range x {
				out[i] = mask(x[i])
			}
			return out
		}
		return v
	}
	expR := exp["reports"].([]any)
	for i, phv := range RC.L("phases") {
		ph := phv.(*pyjson.Map)
		bs := e.bstate("be")
		for _, k := range ph.M("bstate").Keys() {
			if v := ph.M("bstate").V(k); v == nil {
				bs.Delete(k)
			} else {
				bs.Set(k, pyjson.DeepCopy(v))
			}
		}
		for _, n := range ph.M("st").Keys() {
			e.St[n].Update(pyjson.DeepCopy(ph.M("st").V(n)).(*pyjson.Map))
		}
		for _, n := range ph.M("ident").Keys() {
			m := ph.M("ident").M(n)
			e.Ident.Set(n, Identity{m.Str("kind"), m.Str("value"), m.Str("source")})
		}
		for _, gv := range ph.L("gate_res") {
			g := pyjson.StrList(gv)
			e.GateRes[[2]string{g[0], g[1]}] = pyjson.O("build", g[0], "chk", g[1], "result", "confirmed", "user", g[2], "note", g[3])
		}
		want := expR[i].(map[string]any)
		d.same(fmt.Sprintf("reports %d ready", i), want["ready"], gj(t, e.ReportsReady()))
		for _, kind := range []string{"progress", "final"} {
			d.same(fmt.Sprintf("reports %d %s", i, kind), want[kind], mask(gj(t, e.Report(kind, "admin"))))
			d.same(fmt.Sprintf("reports %d %s csv", i, kind), want[kind+" csv"], ReportCSV(e.Report(kind, "admin")))
		}
		P := e.Report("post", "admin")
		P.Set("generated", "2026-11-11T00:00:00Z")
		P.M("final_build").Set("generated", "2026-11-11T00:00:00Z")
		C := e.ReportCertFor(P, "admin")
		d.same(fmt.Sprintf("reports %d post", i), want["post"], gj(t, P))
		d.same(fmt.Sprintf("reports %d post csv", i), want["post csv"], ReportCSV(P))
		d.same(fmt.Sprintf("reports %d post canon", i), want["post canon"], pyjson.Canon(P))
		d.same(fmt.Sprintf("reports %d cert", i), want["cert"], mask(gj(t, C)))
		d.same(fmt.Sprintf("reports %d cert csv", i), want["cert csv"], ReportCSV(C))
	}
	// resilience (HS v2.04): failover, soak, alerts -- plan, submitted text, stored record, evaluations
	expRZ := exp["rz"].([]any)
	for i, cv := range all.L("rz") {
		c := cv.(*pyjson.Map)
		kind := c.Str("kind")
		e.Plan.Set("resilience", pyjson.O(kind, pyjson.DeepCopy(c.V("plan"))))
		e.Results.Delete(kind)
		if c.Truthy("target") {
			e.Conf.Set("TARGET_BUSBW", c.V("target"))
		} else {
			e.Conf.Delete("TARGET_BUSBW")
		}
		var ok bool
		var msg string
		evs := []any{}
		switch kind {
		case "failover":
			ok, msg = e.SubmitFailoverText(c.Str("text"), "op1")
			for _, pv := range c.L("planes") {
				e.Mu.Lock()
				evs = append(evs, e.gateKindEval("failover", pyjson.StrList(pv)))
				e.Mu.Unlock()
			}
		case "soak":
			sw, _ := pyjson.DeepCopy(c.V("switch")).(*pyjson.Map)
			ok, msg = e.SubmitSoakText(c.Str("text"), "op1", sw)
			e.Mu.Lock()
			evs = append(evs, e.gateKindEval("soak", nil))
			e.Mu.Unlock()
		default:
			ok, msg = e.SubmitAlertsText(c.Str("text"), "op1")
			e.Mu.Lock()
			evs = append(evs, e.gateKindEval("alerts", nil))
			e.Mu.Unlock()
		}
		stored := pyjson.NewMap()
		if r := e.Results.M(kind); r != nil {
			stored = r.Copy()
			stored.Delete("ts")
		}
		want := expRZ[i].(map[string]any)
		d.same(fmt.Sprintf("rz %d ret", i), want["ret"], gj(t, []any{ok, msg}))
		d.same(fmt.Sprintf("rz %d eval", i), want["eval"], gj(t, evs))
		d.same(fmt.Sprintf("rz %d stored", i), want["stored"], gj(t, stored))
	}
	t.Logf("scenarios: %d comparisons, %d mismatches", d.n, d.f)
}

func TestPyHelpers(t *testing.T) {
	for in, want := range map[string]float64{" 1_000 ": 1000, "-2.5": -2.5, "1e3": 1000, ".5": 0.5, "5.": 5, "+7": 7} {
		if got, ok := pyFloat(in); !ok || got != want {
			t.Errorf("pyFloat(%q) = %v %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "1__0", "_1", "1_", "0x10", "abc", "1e", "--1"} {
		if _, ok := pyFloat(bad); ok {
			t.Errorf("pyFloat(%q) accepted", bad)
		}
	}
	if got := strings.Join(pySplitlines("a\r\nb\rc\u0085d\n"), "|"); got != "a|b|c|d" {
		t.Errorf("splitlines: %q", got)
	}
}
