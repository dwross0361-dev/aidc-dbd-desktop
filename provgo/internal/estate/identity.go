package estate

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"aidc-dbd/provgo/internal/pyjson"
)

var (
	circSplit  = regexp.MustCompile(`[:|]`)
	ciscoRE    = regexp.MustCompile(`^Cisco\s+(\S+)`)
	aristaRE   = regexp.MustCompile(`\b((?:DCS|CCS)-\S+)`)
	nmRE       = regexp.MustCompile(`[^a-z0-9]`)
	modelPfxRE = regexp.MustCompile(`^(?:cisco|arista|juniper|nvidia|mellanox|nokia)?(?:n9kc|n9k|dcs|ccs|msn)?`)
)

// identMatch returns (device, how) or ("", why).
func (e *Estate) identMatch(ev *pyjson.Map) (string, string) {
	mac, serial := ev.Str("mac"), ev.Str("serial")
	for _, v := range []string{serial, mac} {
		if r, ok := e.Blocked[v]; v != "" && ok {
			return "", "retired: " + r
		}
	}
	e.Ident.index()
	type hit struct {
		pos int
		n   string
		how string
	}
	var hits []hit
	if mac != "" {
		if h, ok := e.Ident.mac[mac]; ok {
			hits = append(hits, hit{h[0].(int), h[1].(string), "MAC"})
		}
	}
	if serial != "" {
		if h, ok := e.Ident.sn[serial]; ok {
			hits = append(hits, hit{h[0].(int), h[1].(string), "serial"})
		}
	}
	if len(hits) > 0 {
		best := hits[0]
		for _, h := range hits[1:] {
			if h.pos < best.pos {
				best = h
			}
		}
		return best.n, best.how
	}
	ip := ev.Str("ip")
	if n, ok := e.ByIP[ip]; ip != "" && ok {
		id, has := e.Ident.Get(n)
		if !ev.Truthy("mac") || !has {
			return n, "reserved address"
		}
		if id.Kind == "sn" {
			return n, "reserved address (serial class)"
		}
	}
	return "", "not in inventory"
}

// Evidence handles one DHCP observation (Kea hook or sniffer).
func (e *Estate) Evidence(ev *pyjson.Map, source string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	e.evidence(ev, source)
}

func (e *Estate) evidence(ev *pyjson.Map, source string) {
	ev.Set("mac", NormMACAny(ev.V("mac")))
	if !ev.Truthy("serial") && ev.Truthy("client_id") {
		ev.Set("serial", SerialFromClientID(ev.Str("client_id")))
	}
	if !ev.Truthy("serial") && ev.Truthy("vendor_class") {
		s, m := SerialFromVendorClass(ev.Str("vendor_class"))
		ev.Set("serial", s)
		ev.Set("model", m)
	}
	n, how := e.identMatch(ev)
	if n == "" {
		if !ev.Truthy("mac") && !ev.Truthy("serial") {
			return
		}
		// HS v2.10: no pin -- bind by cable position (option 82: the OOB access switch and port it called in on)
		pn, pwhy := e.positionMatch(ev)
		if pn == "" {
			e.noteUnclaimed(ev, pwhy, source)
			return
		}
		e.noteUnclaimed(ev, how, source)
		key := ev.Str("mac")
		if key == "" {
			key = ev.Str("serial")
		}
		if u := e.Unclaimed.M(key); u != nil && u.Str("state") == "dismissed" {
			return // dismissed by an operator: never bound by position
		}
		if ok, _ := e.bind(key, pn, "estate (cable position)"); !ok {
			return
		}
		n, how = pn, "cable position"
	}
	s := e.St[n]
	for _, k := range []string{"mac", "serial", "client_id", "circuit"} {
		if ev.Truthy(k) {
			s.Set(k, ev.V(k))
		}
	}
	e.discover(n, ev)
	pid := e.Devs[n].M("id")
	evMac := ev.Str("mac")
	if how == "serial" && pid != nil && pid.Truthy("mac") && evMac != "" && PyStr(s.V("mac_check")) != evMac {
		s.Set("mac_check", evMac)
		pm := NormMACAny(pid.V("mac"))
		if evMac == pm {
			s.Set("id_note", fmt.Sprintf("MAC %s confirmed (manifest)", evMac))
		} else {
			s.Set("id_note", fmt.Sprintf("DHCP MAC %s, manifest MAC %s: recorded, not blocking", evMac, pm))
			e.ev(fmt.Sprintf("%s: matched by serial; its DHCP MAC %s differs from the manifest's %s (recorded)", e.short(n), evMac, pm), "info", n)
		}
	}
	s.Set("last_seen", Now())
	if ev.Truthy("ip") && ev.Truthy("committed") {
		s.Set("lease_ip", ev.V("ip"))
	}
	if !e.waveOpen(n) {
		if s.Str("flag") != "waiting" {
			s.Set("flag", "waiting")
			s.Set("detail", fmt.Sprintf("called in (%s); its wave is not open yet -- holding lease only", how))
			e.ev(fmt.Sprintf("%s: called in before its wave opened (%s)", e.short(n), how), "info", n)
		}
		e.saveDev(n)
		return
	}
	if f := s.Str("flag"); f == "waiting" || f == "slow" {
		s.Set("flag", "")
	}
	if ev.Truthy("committed") {
		d := e.Devs[n]
		ip := ev.V("ip")
		addrs := []any{d.V("ip"), d.V("seed_ip")}
		for _, L := range d.L("ztp_links") {
			addrs = append(addrs, L.(*pyjson.Map).V("ip"))
		}
		in := false
		for _, a := range addrs {
			if pyEq(a, ip) {
				in = true
			}
		}
		if in && s.Int("stage") < 1 {
			e.setStage(n, 1, fmt.Sprintf("lease %s, matched by %s", PyStr(ip), how), "", "")
		}
	}
	e.saveDev(n)
}

// pyEq is == for JSON scalars.
func pyEq(a, b any) bool {
	if pyjson.IsNum(a) && pyjson.IsNum(b) {
		return pyjson.Float(a) == pyjson.Float(b)
	}
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		return ok && x == y
	case nil:
		return b == nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	}
	return false
}

func (e *Estate) noteUnclaimed(ev *pyjson.Map, why, source string) {
	keyV := ev.V("mac")
	if !pyjson.Truthy(keyV) {
		keyV = ev.V("serial")
	}
	key := PyStr(keyV)
	u := e.Unclaimed.M(key)
	isNew := u == nil
	if u == nil {
		u = pyjson.O("key", key, "first", Now(), "state", "open")
	}
	if st := u.Str("state"); st == "bound" || st == "dismissed" {
		return
	}
	for _, k := range []string{"mac", "serial", "client_id", "vendor_class", "model", "circuit", "remote_id", "ip", "giaddr"} {
		if ev.Truthy(k) {
			u.Set(k, ev.V(k))
		}
	}
	u.Set("last", Now())
	u.Set("why", why)
	fo, ok := e.Foreign[ev.Str("serial")]
	if !ok {
		fo, ok = e.Foreign[ev.Str("mac")]
	}
	if ok {
		u.Set("foreign", fmt.Sprintf("%s (%s)", fo[0], fo[1]))
	} else {
		u.Set("foreign", "")
	}
	sg := e.suggest(u)
	u.Set("suggest", sg)
	e.Unclaimed.Set(key, u)
	e.DB.MustExec("INSERT OR REPLACE INTO unclaimed(key,data) VALUES(?,?)", key, pyjson.Dumps(u))
	if isNew {
		kind := "MAC"
		val := ev.V("mac")
		if ev.Truthy("serial") {
			kind = "serial"
			val = ev.V("serial")
		}
		vs := PyStr(val)
		var txt string
		switch {
		case strings.HasPrefix(why, "retired"):
			txt = fmt.Sprintf("refused %s %s: %s", kind, vs, why)
		case u.Str("foreign") != "":
			txt = fmt.Sprintf("refused serial %s: it belongs to site %s", vs, u.Str("foreign"))
		default:
			likely := ""
			if len(sg) > 0 {
				likely = " -- likely " + sg[0].(*pyjson.Map).Str("device")
			}
			txt = fmt.Sprintf("unknown %s %s called in%s", kind, vs, likely)
		}
		dev := ""
		if len(sg) > 0 {
			dev = sg[0].(*pyjson.Map).Str("device")
		}
		e.ev(txt, "warn", dev)
	}
}

func (e *Estate) suggest(u *pyjson.Map) []any {
	out := []any{}
	has := func(n string) bool {
		for _, o := range out {
			if o.(*pyjson.Map).Str("device") == n {
				return true
			}
		}
		return false
	}
	circ := u.Str("circuit")
	if circ != "" {
		parts := circSplit.Split(circ, -1)
		sw := ""
		if len(parts) > 1 {
			sw = parts[0]
		}
		port := NormPort(parts[len(parts)-1]) // HS v2.10: the port is the last field (a relay may add an interface between)
		for _, n := range e.DevOrder {
			ml := e.Devs[n].M("mgmt_link")
			if ml != nil && ml.Len() > 0 && NormPort(ml.Str("port")) == port &&
				(sw == "" || strings.Contains(strings.ToLower(ml.Str("switch")), strings.ToLower(sw))) {
				out = append(out, pyjson.O("device", n, "why", fmt.Sprintf("called in on %s %s, its planned port", PyStr(ml.V("switch")), PyStr(ml.V("port")))))
			}
		}
	}
	model := u.Str("model")
	if len(out) < 3 {
		for _, n := range e.DevOrder {
			if has(n) {
				continue
			}
			d := e.Devs[n]
			if e.stage(n) == 0 && e.waveOpen(n) && model != "" && modelMatch(d.Str("model"), model) {
				out = append(out, pyjson.O("device", n, "why", "same model, open wave, not yet called in"))
			}
			if len(out) >= 5 {
				break
			}
		}
	}
	return out
}

// discover compares the reported model and switch port with the plan (HS-DEP-003 step 4.4).
func (e *Estate) discover(n string, ev *pyjson.Map) {
	s, d := e.St[n], e.Devs[n]
	vc := ev.Str("vendor_class")
	seen := ev.Str("model")
	if seen == "" && vc != "" {
		if m := ciscoRE.FindStringSubmatch(vc); m != nil {
			seen = m[1]
		} else if m := aristaRE.FindStringSubmatch(vc); m != nil {
			seen = m[1]
		}
	}
	dz, _ := s.V("disc").(*pyjson.Map)
	if dz == nil {
		dz = pyjson.NewMap()
		s.Set("disc", dz)
	}
	if seen != "" && d.Truthy("model") {
		ok := modelMatch(d.Str("model"), seen)
		dz.Set("model", pyjson.O("seen", seen, "planned", d.V("model"), "ok", ok))
	}
	circ := ev.Str("circuit")
	ml := d.M("mgmt_link")
	if circ != "" && ml != nil && ml.Truthy("port") {
		parts := circSplit.Split(circ, -1)
		port := NormPort(parts[len(parts)-1])
		sw := ""
		if len(parts) > 1 {
			sw = parts[0]
		}
		mlsw := strings.ToLower(ml.Str("switch"))
		ok := port == NormPort(ml.Str("port")) && (sw == "" || strings.Contains(mlsw, strings.ToLower(sw)) || strings.HasSuffix(mlsw, strings.ToLower(sw)))
		dz.Set("port", pyjson.O("seen", circ, "planned", fmt.Sprintf("%s %s", ml.Str("switch"), PyStr(ml.V("port"))), "ok", ok))
	}
	var bad []string
	for _, k := range []string{"model", "port"} {
		if x := dz.M(k); x != nil && x.Len() > 0 {
			if okv, isB := x.V("ok").(bool); isB && !okv {
				bad = append(bad, k)
			}
		}
	}
	if len(bad) > 0 && !s.Truthy("disc_ok") && s.Str("flag") != "failed" && s.Int("stage") < 4 {
		var parts []string
		for _, k := range bad {
			x := dz.M(k)
			parts = append(parts, fmt.Sprintf("%s %s, planned %s", k, PyStr(x.V("seen")), PyStr(x.V("planned"))))
		}
		why := strings.Join(parts, "; ")
		s.Set("flag", "failed")
		s.Set("detail", "discovery: "+why+" -- fix the unit or its cable, then Retry (Retry also accepts it as is)")
		e.ev(fmt.Sprintf("%s: discovery mismatch: %s", e.short(n), why), "fail", n)
	}
}

// ParseHook turns a Kea run_script environment dump into evidence.
func ParseHook(body []byte, event string) []*pyjson.Map {
	env := map[string]string{}
	text := strings.ToValidUTF8(string(body), "�")
	for _, ln := range splitLinesPy(text) {
		if i := strings.Index(ln, "="); i >= 0 {
			env[pyStrip(ln[:i])] = pyStrip(ln[i+1:])
		}
	}
	var out []*pyjson.Map
	switch event {
	case "leases4_committed":
		sz := env["LEASES4_SIZE"]
		if sz == "" {
			sz = "0"
		}
		cnt, err := pyInt(sz)
		if err != nil {
			panic(err) // Python raises: the hook gets no reply
		}
		for i := int64(0); i < cnt; i++ {
			p := fmt.Sprintf("LEASES4_AT%d_", i)
			out = append(out, pyjson.O("ip", env[p+"ADDRESS"], "mac", env[p+"HWADDR"], "client_id", env[p+"CLIENT_ID"], "committed", true))
		}
	case "lease4_renew", "lease4_rebind":
		out = append(out, pyjson.O("ip", env["LEASE4_ADDRESS"], "mac", env["LEASE4_HWADDR"], "client_id", env["LEASE4_CLIENT_ID"], "committed", true))
	}
	for _, e := range out {
		c, r := env["QUERY4_OPTION_82_SUB_OPTION_1"], env["QUERY4_OPTION_82_SUB_OPTION_2"]
		if c != "" {
			if p := Printable(HexToBytes(c)); p != "" {
				e.Set("circuit", p)
			} else {
				e.Set("circuit", c)
			}
		}
		if r != "" {
			if p := Printable(HexToBytes(r)); p != "" {
				e.Set("remote_id", p)
			} else {
				e.Set("remote_id", r)
			}
		}
		e.Set("giaddr", env["QUERY4_GIADDR"])
	}
	return out
}

// splitLinesPy is str.splitlines().
func splitLinesPy(s string) []string {
	var out []string
	start := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\n', '\x0b', '\x0c', '\x1c', '\x1d', '\x1e', '\x85', ' ', ' ':
			out = append(out, string(rs[start:i]))
			start = i + 1
		case '\r':
			out = append(out, string(rs[start:i]))
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}

// ParseDHCP reads an Ethernet frame carrying a DHCP client message, or returns nil.
func ParseDHCP(frame []byte) *pyjson.Map {
	if len(frame) < 14+20+8+240 || frame[12] != 0x08 || frame[13] != 0x00 {
		return nil
	}
	ip := frame[14:]
	ihl := int(ip[0]&15) * 4
	if ip[9] != 17 || len(ip) < ihl+8 {
		return nil
	}
	udp := ip[ihl:]
	if binary.BigEndian.Uint16(udp[2:4]) != 67 {
		return nil
	}
	b := udp[8:]
	if len(b) < 240 || string(b[236:240]) != "\x63\x82\x53\x63" {
		return nil
	}
	macs := make([]string, 6)
	for i := 0; i < 6; i++ {
		macs[i] = fmt.Sprintf("%02x", b[28+i])
	}
	ev := pyjson.O("mac", strings.Join(macs, ":"), "giaddr", net.IP(b[24:28]).String(), "ciaddr", net.IP(b[12:16]).String())
	i := 240
	for i < len(b) {
		code := b[i]
		if code == 255 {
			break
		}
		if code == 0 {
			i++
			continue
		}
		if i+1 >= len(b) {
			break
		}
		ln := int(b[i+1])
		end := i + 2 + ln
		if end > len(b) {
			end = len(b)
		}
		val := b[i+2 : end]
		i += 2 + ln
		switch code {
		case 53:
			if len(val) > 0 {
				ev.Set("type", int64(val[0]))
			}
		case 60:
			ev.Set("vendor_class", Printable(val))
		case 61:
			ev.Set("client_id", hex.EncodeToString(val))
		case 12:
			ev.Set("hostname", Printable(val))
		case 50:
			if ln == 4 && len(val) == 4 {
				ev.Set("req_ip", net.IP(val).String())
			} else {
				ev.Set("req_ip", "")
			}
		case 82:
			j := 0
			for j+2 <= len(val) {
				sc, sl := val[j], int(val[j+1])
				se := j + 2 + sl
				if se > len(val) {
					se = len(val)
				}
				sv := val[j+2 : se]
				if sc == 1 {
					if p := Printable(sv); p != "" {
						ev.Set("circuit", p)
					} else {
						ev.Set("circuit", hex.EncodeToString(sv))
					}
				} else if sc == 2 {
					if p := Printable(sv); p != "" {
						ev.Set("remote_id", p)
					} else {
						ev.Set("remote_id", hex.EncodeToString(sv))
					}
				}
				j += 2 + sl
			}
		}
	}
	return ev
}

var _ = strconv.Itoa

// modelKey is a model as compared (HS v2.09): letters and digits only, without a vendor name or the PID prefixes a
// device may or may not report (DCS-, CCS-, N9K-C, MSN), so DCS-7060X6-64PE-F matches DCS-7060X6-64PE.
func modelKey(x string) string {
	return modelPfxRE.ReplaceAllString(nmRE.ReplaceAllString(strings.ToLower(x), ""), "")
}

// modelMatch: the planned and the seen model, either within the other.
func modelMatch(planned, seen string) bool {
	a, b := modelKey(planned), modelKey(seen)
	return a != "" && b != "" && (strings.Contains(b, a) || strings.Contains(a, b))
}

// positionMatch (HS v2.10): the device planned on the port this unit called in on (option 82), or "" and why it is held.
func (e *Estate) positionMatch(ev *pyjson.Map) (string, string) {
	circ := ev.Str("circuit")
	if circ == "" {
		return "", "not in inventory"
	}
	parts := circSplit.Split(circ, -1)
	sw := ""
	if len(parts) > 1 {
		sw = parts[0]
	}
	last := parts[len(parts)-1]
	port := NormPort(last)
	at := last
	if sw != "" {
		at = sw + " " + last
	}
	var cand []string
	for _, n := range e.DevOrder {
		ml := e.Devs[n].M("mgmt_link")
		if ml != nil && ml.Truthy("port") && NormPort(ml.Str("port")) == port &&
			(sw == "" || strings.Contains(strings.ToLower(ml.Str("switch")), strings.ToLower(sw))) {
			cand = append(cand, n)
		}
	}
	if len(cand) == 0 {
		return "", fmt.Sprintf("not in inventory; %s is planned for no device", at)
	}
	if len(cand) > 1 {
		return "", fmt.Sprintf("not in inventory; %s matches %d planned devices: bind it by hand", at, len(cand))
	}
	n := cand[0]
	d := e.Devs[n]
	if cur, ok := e.Ident.Get(n); ok {
		return "", fmt.Sprintf("held: %s is %s's position, already held by %s %s", at, e.short(n), idWord(cur.Kind), cur.Value)
	}
	if ev.Truthy("model") && d.Truthy("model") && !modelMatch(d.Str("model"), ev.Str("model")) {
		return "", fmt.Sprintf("held: on %s, %s's position, but reports model %s, planned %s", at, e.short(n), ev.Str("model"), d.Str("model"))
	}
	return n, "cable position"
}
