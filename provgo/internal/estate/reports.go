package estate

// Reports (HS v2.02, HS-DEP-001 section 7.3; Python: REPORT_TITLES, _rep_*, report, report_cert_for, reports_ready,
// canon, report_csv, report_html, report_files, report_template). Four reports from the console's own record: a
// progress build report (any time), the final build report (the build closed), the post-deployment report (acceptance)
// and the Certificate of Conformance. Each is JSON, CSV and a print-ready page; the hand-over export carries the last three.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"aidc-dbd/provgo/internal/pyjson"
)

// ReportTitles is REPORT_TITLES.
var ReportTitles = map[string]string{"progress": "Progress build report", "final": "Final build report",
	"post": "Post-deployment report", "cert": "Certificate of Conformance"}

// ReportFiles is REPORT_FILES: the file name of each report.
var ReportFiles = map[string]string{"progress": "progress-build-report", "final": "final-build-report",
	"post": "post-deployment-report", "cert": "certificate-of-conformance"}

var acceptKinds = map[string]bool{"accept": true, "tuned": true, "isolation": true, "inference": true}

type repDev struct{ n, build string }

func (e *Estate) repOrder() []repDev {
	seen := map[string]bool{}
	var out []repDev
	for _, b := range e.Builds {
		for _, w := range b.L("waves") {
			for _, n := range pyjson.StrList(w.(*pyjson.Map).V("devices")) {
				if !seen[n] {
					seen[n] = true
					out = append(out, repDev{n, b.Str("name")})
				}
			}
		}
	}
	for _, n := range e.DevOrder {
		if !seen[n] {
			out = append(out, repDev{n, ""})
		}
	}
	return out
}

func (e *Estate) isAgg(n string) bool { return e.Devs[n].Truthy("aggregate") }

func (e *Estate) repCounts(names []string) *pyjson.Map {
	var tot, done, act, wait, flg, aside int64
	for _, n := range names {
		s := e.St[n]
		tot++
		f := s.Str("flag")
		switch {
		case f == "quarantined":
			aside++
		case s.Int("stage") >= 6:
			done++
		case f != "":
			flg++
		case s.Int("stage") >= 1:
			act++
		default:
			wait++
		}
	}
	return pyjson.O("total", tot, "done", done, "active", act, "waiting", wait, "flagged", flg, "set_aside", aside)
}

func (e *Estate) repGate(k string) []any {
	bs := e.bstate(k)
	b := e.BuildByKey[k]
	out := []any{}
	if bs.Str("state") != "queued" {
		for _, r := range e.gateEval(k) {
			out = append(out, pyjson.O("key", r.V("key"), "label", r.V("label"), "type", r.V("type"), "kind", dget(r, "kind", ""),
				"result", r.V("result"), "note", dget(r, "note", "")))
		}
		return out
	}
	for _, cv := range b.L("gate") {
		c := cv.(*pyjson.Map)
		out = append(out, pyjson.O("key", c.V("key"), "label", c.V("label"), "type", c.V("type"), "kind", dget(c, "kind", ""),
			"result", "waiting", "note", dget(c, "note", "")))
	}
	return out
}

func (e *Estate) repBuilds() []any {
	out := []any{}
	for _, b := range e.Builds {
		bs := e.bstate(b.Str("key"))
		var names []string
		for _, w := range b.L("waves") {
			names = append(names, pyjson.StrList(w.(*pyjson.Map).V("devices"))...)
		}
		var ov any
		if o := bs.M("override"); o != nil && o.Len() > 0 {
			ov = pyjson.O("by", dget(o, "user", ""), "reason", dget(o, "reason", ""), "when", isoAny(o.V("ts")))
		}
		out = append(out, pyjson.O("key", b.V("key"), "name", b.V("name"), "state", bs.V("state"), "waves", int64(len(b.L("waves"))),
			"waves_open", dget(bs, "open_waves", int64(0)), "counts", e.repCounts(names), "gate", e.repGate(b.Str("key")),
			"override", ov, "passed", isoAny(bs.V("passed"))))
	}
	return out
}

func (e *Estate) repHead(kind, user string) *pyjson.Map {
	S := e.site()
	site := pyjson.NewMap()
	for _, k := range []string{"build", "customer", "code", "region", "region_name", "delivered_by", "builder",
		"fingerprint", "plan_digest", "racks", "gpus", "workload"} {
		site.Set(k, dget(S, k, ""))
	}
	return pyjson.O("kind", kind, "title", ReportTitles[kind], "generated", ISO(Now()), "by", user, "site", site,
		"standards", dget(e.Plan, "standards", []any{}))
}

func (e *Estate) repSoftware() []any {
	cnt := map[[2]string]int64{}
	for _, n := range e.DevOrder {
		d := e.Devs[n]
		if d.Truthy("aggregate") {
			continue
		}
		rel := d.Str("release")
		if m := d.M("release"); m != nil { // the release it was built to
			rel = m.Str("target")
		}
		cnt[[2]string{d.Str("os"), rel}]++
	}
	keys := make([][2]string, 0, len(cnt))
	for k := range cnt {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	out := []any{}
	for _, k := range keys {
		out = append(out, pyjson.O("os", k[0], "release", k[1], "devices", cnt[k]))
	}
	return out
}

func (e *Estate) repRun() *pyjson.Map {
	return pyjson.O("state", dget(e.Run, "state", ""), "started", isoAny(e.Run.V("started")))
}

func (e *Estate) live(order []repDev) []string {
	var out []string
	for _, x := range order {
		if !e.isAgg(x.n) {
			out = append(out, x.n)
		}
	}
	return out
}

func detail160(s *pyjson.Map) string { return pyHead(s.Str("detail"), 160) }

func rmaOf(s *pyjson.Map) *pyjson.Map {
	if r := s.M("rma"); r != nil && r.Len() > 0 {
		return r
	}
	return nil
}

func (e *Estate) repProgress(user string) *pyjson.Map {
	R := e.repHead("progress", user)
	order := e.repOrder()
	live := e.live(order)
	counts := e.repCounts(live)
	R.Set("counts", counts)
	R.Set("run", e.repRun())
	R.Set("builds", e.repBuilds())
	rows := []any{}
	rix := map[string]*pyjson.Map{}
	for _, n := range live {
		r := e.Devs[n].Str("row")
		if r == "" {
			continue
		}
		m := rix[r]
		if m == nil {
			m = pyjson.O("row", r, "total", int64(0), "done", int64(0))
			rix[r] = m
			rows = append(rows, m)
		}
		m.Set("total", m.Int("total")+1)
		if e.stage(n) >= 6 && e.flag(n) != "quarantined" {
			m.Set("done", m.Int("done")+1)
		}
	}
	R.Set("rows", rows)
	blocked := []any{}
	for _, x := range order {
		if f := e.flag(x.n); !e.isAgg(x.n) && (f == "failed" || f == "missing" || f == "down") {
			blocked = append(blocked, pyjson.O("device", x.n, "build", x.build, "flag", f, "detail", detail160(e.St[x.n])))
		}
	}
	R.Set("blocked_n", int64(len(blocked)))
	if len(blocked) > 300 {
		blocked = blocked[:300]
	}
	R.Set("blocked", blocked)
	rmas := []any{}
	for _, x := range order {
		if r := rmaOf(e.St[x.n]); r != nil && r.Str("state") == "open" {
			rmas = append(rmas, pyjson.O("device", x.n, "ticket", dget(r, "ticket", "")))
		}
	}
	R.Set("rmas", rmas)
	t := Now()
	holds := []any{}
	for _, x := range order {
		if h, ok := e.holds[x.n]; ok && h.until > t {
			holds = append(holds, pyjson.O("device", x.n, "user", h.user))
		}
	}
	R.Set("holds", holds)
	last8 := int64(0)
	for _, n := range live {
		if e.stage(n) < 6 {
			continue
		}
		if ts := e.St[n].M("ts"); ts != nil && pyjson.Truthy(ts.V("6")) && pyjson.Float(ts.V("6")) >= t-8*3600 {
			last8++
		}
	}
	remaining := counts.Int("total") - counts.Int("done") - counts.Int("set_aside")
	var eta any
	if last8 > 0 && remaining > 0 {
		eta = (remaining*8 + last8 - 1) / last8
	}
	R.Set("pace", pyjson.O("done_last_8h", last8, "remaining", remaining, "eta_hours", eta))
	act := map[string]int64{}
	for _, r := range e.DB.Query("SELECT user, COUNT(*) AS n FROM audit WHERE ts>=? GROUP BY user", t-12*3600) {
		act[r.Str("user")] = pyjson.Int(r.V("n"))
	}
	users := make([]string, 0, len(act))
	for u := range act {
		users = append(users, u)
	}
	sort.Strings(users)
	al := []any{}
	for _, u := range users {
		al = append(al, pyjson.O("user", u, "actions", act[u]))
	}
	R.Set("activity_12h", al)
	return R
}

func (e *Estate) repFinal(user string) *pyjson.Map {
	R := e.repHead("final", user)
	order := e.repOrder()
	live := e.live(order)
	R.Set("counts", e.repCounts(live))
	R.Set("run", e.repRun())
	builds := e.repBuilds()
	R.Set("builds", builds)
	var mx float64
	np := 0
	for _, b := range e.Builds {
		if p := e.bstate(b.Str("key")).V("passed"); pyjson.Truthy(p) {
			np++
			if f := pyjson.Float(p); f > mx {
				mx = f
			}
		}
	}
	completed := ""
	if np > 0 && np == len(e.Builds) {
		completed = ISO(mx)
	}
	R.Set("completed", completed)
	dev, opn := []any{}, []any{}
	for _, bv := range builds {
		b := bv.(*pyjson.Map)
		ov := b.M("override")
		if ov != nil {
			dev = append(dev, pyjson.O("type", "gate override", "build", b.V("name"), "item", "build gate", "detail", ov.V("reason"),
				"by", ov.V("by"), "when", ov.V("when")))
		}
		for _, gv := range b.L("gate") {
			g := gv.(*pyjson.Map)
			res := g.Str("result")
			if res == "warn" {
				dev = append(dev, pyjson.O("type", "accepted warning", "build", b.V("name"), "item", g.V("label"), "detail", g.V("note"), "by", "", "when", ""))
			} else if res != "pass" && res != "confirmed" && ov == nil {
				opn = append(opn, pyjson.O("build", b.V("name"), "item", g.V("label"), "result", g.V("result"), "detail", g.V("note")))
			}
		}
	}
	for _, x := range order {
		if e.flag(x.n) == "quarantined" && !e.isAgg(x.n) {
			dev = append(dev, pyjson.O("type", "set aside", "build", x.build, "item", x.n, "detail", detail160(e.St[x.n]), "by", "", "when", ""))
		}
	}
	R.Set("deviations", dev)
	R.Set("open", opn)
	nd := []any{}
	for _, n := range live {
		if e.stage(n) < 6 && e.flag(n) != "quarantined" {
			nd = append(nd, n)
		}
	}
	R.Set("not_done_n", int64(len(nd)))
	if len(nd) > 100 {
		nd = nd[:100]
	}
	R.Set("not_done", nd)
	ab := []any{}
	for _, n := range live {
		if id, ok := e.Ident.Get(n); ok && id.Source != "inventory" {
			ab = append(ab, pyjson.O("device", n, "identity", id.Value, "source", id.Source))
		}
	}
	if len(ab) > 1000 {
		ab = ab[:1000]
	}
	R.Set("asbuilt", ab)
	rmas := []any{}
	for _, n := range live {
		if r := rmaOf(e.St[n]); r != nil {
			rmas = append(rmas, pyjson.O("device", n, "ticket", dget(r, "ticket", ""), "state", dget(r, "state", "")))
		}
	}
	R.Set("rmas", rmas)
	R.Set("software", e.repSoftware())
	return R
}

func (e *Estate) repPost(user string) *pyjson.Map {
	R := e.repHead("post", user)
	F := e.repFinal(user)
	R.Set("counts", F.V("counts"))
	R.Set("completed", F.V("completed"))
	acc := []any{}
	for _, bv := range F.L("builds") {
		b := bv.(*pyjson.Map)
		for _, gv := range b.L("gate") {
			g := gv.(*pyjson.Map)
			if t := g.Str("type"); t != "auto" && t != "confirm" {
				continue
			}
			evl := []any{}
			if k := g.Str("kind"); acceptKinds[k] {
				var r *pyjson.Map
				switch k {
				case "accept":
					r = e.acceptEval()
				case "tuned":
					r = e.tunedEval()
				case "inference":
					r = e.inferenceEval()
				default:
					r = e.isolationEval()
				}
				if ev := r.M("evidence"); ev != nil {
					for _, c := range ev.Keys() {
						x, _ := ev.V(c).(*pyjson.Map)
						if x == nil {
							x = pyjson.NewMap()
						}
						evl = append(evl, pyjson.O("check", c, "ok", x.Truthy("ok"), "note", dget(x, "note", "")))
					}
				}
				if len(evl) > 400 {
					evl = evl[:400]
				}
			}
			acc = append(acc, pyjson.O("build", b.V("name"), "item", g.V("label"), "result", g.V("result"), "detail", g.V("note"), "evidence", evl))
		}
	}
	R.Set("acceptance", acc)
	res := []any{}
	keys := append([]string(nil), e.Results.Keys()...)
	sort.Strings(keys)
	for _, k := range keys {
		v, ok := e.Results.V(k).(*pyjson.Map)
		if !ok || !v.Has("busbw") {
			continue
		}
		res = append(res, pyjson.O("test", k, "busbw", v.V("busbw"), "target", v.V("target"), "by", dget(v, "by", ""), "when", isoAny(v.V("ts"))))
	}
	R.Set("results", res)
	var tuning any
	if tv, ok := e.tuningView().(*pyjson.Map); ok && tv != nil {
		var ap any
		if a := tv.M("approved"); a != nil && a.Len() > 0 {
			ap = pyjson.O("v", a.V("v"), "by", dget(a, "by", ""), "when", isoAny(a.V("ts")))
		}
		tuning = pyjson.O("versions", int64(len(tv.L("versions"))), "switches", tv.V("switches"), "approved", ap)
	}
	R.Set("tuning", tuning)
	R.Set("software", F.V("software"))
	R.Set("deviations", F.V("deviations"))
	R.Set("open", F.V("open"))
	R.Set("final_build", F)
	return R
}

func (e *Estate) repCert(user string, P *pyjson.Map) *pyjson.Map {
	R := e.repHead("cert", user)
	if P == nil {
		P = e.repPost(user)
	}
	F := P.M("final_build")
	why := []any{}
	if F.Str("completed") == "" {
		why = append(why, "not every build has passed its gate")
	}
	if n := F.Int("not_done_n"); n > 0 {
		why = append(why, fmt.Sprintf("%d device(s) not built", n))
	}
	if o := F.L("open"); len(o) > 0 {
		why = append(why, fmt.Sprintf("%d gate item(s) open", len(o)))
	}
	status := "conformant"
	if len(why) > 0 {
		status = "not issued"
	} else if len(F.L("deviations")) > 0 {
		status = "conformant with deviations"
	}
	R.Set("status", status)
	R.Set("reasons", why)
	R.Set("dates", pyjson.O("started", F.M("run").V("started"), "accepted", F.V("completed"), "issued", P.V("generated")))
	c, s := F.M("counts"), R.M("site")
	R.Set("totals", pyjson.O("devices", c.V("total"), "built", c.V("done"), "set_aside", c.V("set_aside"), "racks", s.V("racks"), "gpus", s.V("gpus")))
	acc := []any{}
	for _, av := range P.L("acceptance") {
		a := av.(*pyjson.Map)
		acc = append(acc, pyjson.O("build", a.V("build"), "item", a.V("item"), "result", a.V("result")))
	}
	R.Set("acceptance", acc)
	R.Set("deviations", F.V("deviations"))
	sum := sha256.Sum256([]byte(pyjson.Canon(P)))
	R.Set("post_sha256", hex.EncodeToString(sum[:]))
	return R
}

func (e *Estate) report(kind, user string) *pyjson.Map {
	switch kind {
	case "progress":
		return e.repProgress(user)
	case "final":
		return e.repFinal(user)
	case "post":
		return e.repPost(user)
	case "cert":
		return e.repCert(user, nil)
	}
	return nil
}

// Report is report(kind, user): nil for an unknown kind.
func (e *Estate) Report(kind, user string) *pyjson.Map {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	return e.report(kind, user)
}

// ReportCertFor is report_cert_for: the certificate resting on this post-deployment report.
func (e *Estate) ReportCertFor(P *pyjson.Map, user string) *pyjson.Map {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	return e.repCert(user, P)
}

func (e *Estate) reportsReady() *pyjson.Map {
	var left []string
	for _, b := range e.Builds {
		if e.bstate(b.Str("key")).Str("state") != "complete" {
			left = append(left, b.Str("name"))
		}
	}
	nd := 0
	for _, n := range e.DevOrder {
		if !e.isAgg(n) && e.stage(n) < 6 && e.flag(n) != "quarantined" {
			nd++
		}
	}
	why := ""
	if len(left) > 0 {
		why = "gate not passed: " + strings.Join(left, ", ")
	}
	cw := why
	if cw == "" && nd > 0 {
		cw = fmt.Sprintf("%d device(s) not built", nd)
	}
	return pyjson.O("progress", pyjson.O("ok", true, "why", ""), "final", pyjson.O("ok", why == "", "why", why),
		"post", pyjson.O("ok", why == "", "why", why), "cert", pyjson.O("ok", cw == "", "why", cw))
}

// ReportsReady is reports_ready: what the console's Reports menu offers now.
func (e *Estate) ReportsReady() *pyjson.Map {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	return e.reportsReady()
}

// ReportCSV is report_csv.
func ReportCSV(R *pyjson.Map) string {
	w := &CSVWriter{}
	switch R.Str("kind") {
	case "progress":
		w.Row("build", "state", "total", "done", "active", "waiting", "flagged", "set_aside")
		for _, bv := range R.L("builds") {
			b := bv.(*pyjson.Map)
			c := b.M("counts")
			w.Row(b.V("name"), b.V("state"), c.V("total"), c.V("done"), c.V("active"), c.V("waiting"), c.V("flagged"), c.V("set_aside"))
		}
	case "final":
		w.Row("type", "build", "item", "detail", "by", "when")
		for _, dv := range R.L("deviations") {
			d := dv.(*pyjson.Map)
			w.Row(d.V("type"), d.V("build"), d.V("item"), d.V("detail"), d.V("by"), d.V("when"))
		}
	case "post":
		w.Row("build", "item", "result", "detail")
		for _, av := range R.L("acceptance") {
			a := av.(*pyjson.Map)
			w.Row(a.V("build"), a.V("item"), a.V("result"), a.V("detail"))
		}
	default:
		w.Row("item", "value")
		d := R.M("dates")
		w.Row("status", R.V("status"))
		w.Row("site", R.M("site").V("build"))
		w.Row("accepted", d.V("accepted"))
		w.Row("issued", d.V("issued"))
		w.Row("post_sha256", R.V("post_sha256"))
	}
	return w.String()
}

// ReportHTML is report_html: the print-ready page, the certificate appended to a post-deployment report; "" without
// the template (console.html carries it).
func (e *Estate) ReportHTML(R, cert *pyjson.Map) (string, bool) {
	if !strings.Contains(e.ReportTpl, "__REPORT_DATA__") {
		return "", false
	}
	if cert != nil {
		R = R.Copy()
		R.Set("_cert", cert)
	}
	return strings.Replace(e.ReportTpl, "__REPORT_DATA__", strings.ReplaceAll(pyjson.Dumps(R), "</", `<\/`), -1), true
}

type namedText struct{ name, text string }

// reportFiles is report_files (lock held): the hand-over export's reports/ folder.
func (e *Estate) reportFiles(user string) []namedText {
	F, P := e.report("final", user), e.report("post", user)
	C := e.repCert(user, P)
	var out []namedText
	for _, x := range []struct {
		name    string
		R, cert *pyjson.Map
	}{{"final-build-report", F, nil}, {"post-deployment-report", P, C}, {"certificate-of-conformance", C, nil}} {
		out = append(out, namedText{x.name + ".json", pyjson.Canon(x.R)})
		if h, ok := e.ReportHTML(x.R, x.cert); ok {
			out = append(out, namedText{x.name + ".html", h})
		}
	}
	return out
}

var reportTplRE = regexp.MustCompile(`id="report-tpl-b64">([A-Za-z0-9+/=\s]+)<`)
var wsRE = regexp.MustCompile(`\s`)

// ReportTemplate is report_template: the report page carried base64 in console.html.
func ReportTemplate(consoleHTML string) string {
	m := reportTplRE.FindStringSubmatch(consoleHTML)
	if m == nil {
		return ""
	}
	b, err := base64.StdEncoding.DecodeString(wsRE.ReplaceAllString(m[1], ""))
	if err != nil {
		return ""
	}
	return string(b)
}
