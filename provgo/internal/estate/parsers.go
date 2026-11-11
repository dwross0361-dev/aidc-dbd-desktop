package estate

// Device output parsers and the per-OS command sets of the reader (Phase 2). Each function mirrors its Python
// namesake in estate_build.py; the unit tests compare them on recorded vendor output.

import (
	"math/big"
	"regexp"
	"sort"
	"strings"

	"aidc-dbd/provgo/internal/pyjson"
)

// DT is the netmiko device type of each OS the reader supports.
var DT = map[string]string{"junos": "juniper_junos", "eos": "arista_eos", "nxos": "cisco_nxos", "cumulus": "linux", "srl": "nokia_srl",
	"sros": "nokia_sros", "iosxr": "cisco_xr", "iosxe": "cisco_xe", "ftd": "cisco_ftd"}

// CMDS are the commands of the BGP, EVPN, HA and RoCE checks per OS.
var CMDS = map[string]map[string][]string{
	"bgp": {"junos": {"show bgp summary | display json"}, "eos": {"show ip bgp summary vrf all | json"},
		"nxos": {"show ip bgp summary vrf all | json"}, "cumulus": {"@nvue-bgp"},
		"srl":  {"info from state network-instance * protocols bgp neighbor * session-state | as json"},
		"sros": {"show router bgp summary all"}, "iosxr": {"show bgp vrf all summary"}, "ftd": {"show bgp summary"}},
	"evpn": {"junos": {"show bgp summary | display json"}, "eos": {"show bgp evpn summary | json"},
		"nxos": {"show bgp l2vpn evpn summary | json"}, "cumulus": {"@nvue-bgp"},
		"srl":  {"info from state network-instance * protocols bgp neighbor * session-state | as json"},
		"sros": {"show router bgp summary family evpn"}, "iosxr": {"show bgp l2vpn evpn summary"}},
	"mnha": {"junos": {"show chassis high-availability information"}, "ftd": {"show failover"}},
	"roce": {"cumulus": {"nv show qos roce -o json"},
		"junos": {"show configuration class-of-service | display set | match \"pfc|explicit-congestion|ecn\""},
		"eos":   {"show priority-flow-control"}, "nxos": {"show interface priority-flow-control"},
		"srl": {"info flat qos"}},
}

// CNTCMDS are the lossless acceptance counter commands per OS.
var CNTCMDS = map[string][]string{
	"cumulus": {"@per-if:nv show interface {if} counters qos -o json", "nv show qos pfc-watchdog -o json"},
	"junos":   {"show interfaces queue | display json", "show interfaces extensive | display json"},
	"eos": {"show priority-flow-control counters detail | json", "show interfaces counters queue detail | json",
		"show priority-flow-control counters watchdog | json"},
	"nxos": {"show interface priority-flow-control detail | json", "show queuing interface | json", "show queuing pfc-queue detail | json"},
}

// VersionCmd, LLDPCmd and BackupCmd are the release, LLDP and configuration commands per OS.
var VersionCmd = map[string]string{"cumulus": "cat /etc/lsb-release", "junos": "show version", "eos": "show version", "nxos": "show version",
	"srl": "info from state system information version", "sros": "show version", "iosxr": "show version", "iosxe": "show version"}
var LLDPCmd = map[string]string{"cumulus": "nv show interface lldp -o json", "junos": "show lldp neighbors | display json", "eos": "show lldp neighbors | json",
	"nxos": "show lldp neighbors detail | json", "srl": "info from state system lldp interface * neighbor * | as json"}
var BackupCmd = map[string]string{"junos": "show configuration | display set", "eos": "show running-config", "nxos": "show running-config",
	"cumulus": "nv config show -o commands", "srl": "info flat /", "sros": "admin show configuration /configure flat",
	"iosxr": "show running-config", "iosxe": "show running-config"}

var (
	losslessRE = regexp.MustCompile(`(?i)(\bmtu\b|pfc|priority-flow-control|\becn\b|explicit-congestion|congestion-notification|random-detect|qos roce|` +
		`watchdog|no-loss|lossless|class-of-service|\bqos\b)`)
	accQ3RE    = pyRe(`(prio(rity)?|pg|tc|traffic[-_ ]?class|queue|cos|fc)[-_ /]?3(\D|$)`)
	bgpStateRE = pyRe(`(?i)^(established|idle|active|connect|opensent|openconfirm)\b`)
	syncRE     = pyRe(`(?i)in sync|synchroni[sz]ed|sync(hronization)? status\s*:\s*(complete|ok)`)
	slugRE     = regexp.MustCompile(`[^A-Za-z0-9]+`)
	adminRE    = regexp.MustCompile(`(?i)admin`)
	ifKeyRE    = pyRe(`^(swp|eth|Ethernet|et-|xe-|ge-|mgmt|e\d|ethernet-)\S*$`)
	lldpKVRE   = pyRe(`^lldp\.([^.]+)\.(chassis\.name|port\.ifname|port\.descr)=(.*)$`)
)

// Slug is the file name of a recorded command output (reader simulation).
func Slug(c string) string {
	s := strings.Trim(slugRE.ReplaceAllString(c, "_"), "_")
	if len(s) > 90 {
		s = s[:90]
	}
	return s
}

// unwrap takes single-element lists apart (and {"data": x} inside them), as Junos JSON wraps every value.
func unwrap(v any) any {
	for {
		l, ok := v.([]any)
		if !ok || len(l) != 1 {
			return v
		}
		v = l[0]
		if m, ok := v.(*pyjson.Map); ok && m.Len() == 1 && m.Has("data") {
			v = m.V("data")
		}
	}
}

func isContainer(v any) bool {
	switch v.(type) {
	case *pyjson.Map, []any:
		return true
	}
	return false
}

// BGPSess is one session: peer and state.
type BGPSess struct{ Peer, State string }

// jsonBGP collects every BGP session in a vendor JSON tree; admin-down sessions are left out.
func jsonBGP(obj any, out *[]BGPSess, hint string) {
	if l, ok := obj.([]any); ok {
		for _, x := range l {
			jsonBGP(x, out, hint)
		}
		return
	}
	m, ok := obj.(*pyjson.Map)
	if !ok {
		return
	}
	admin := false
	for _, k := range m.Keys() {
		u := unwrap(m.V(k))
		if isContainer(u) {
			continue
		}
		s := PyStr(u)
		if runeLen(s) < 40 && adminRE.MatchString(s) {
			admin = true
		}
	}
	for _, k := range []string{"peer-state", "peerState", "state", "session-state", "peerstate", "connection-state"} {
		if !m.Has(k) {
			continue
		}
		v, isS := unwrap(m.V(k)).(string)
		if isS && bgpStateRE.MatchString(pyStripU(v)) {
			if !admin {
				var peer any
				for _, pk := range []string{"peer-address", "neighborid", "neighbor-id", "peerAddress", "neighbor"} {
					if x := m.V(pk); pyjson.Truthy(x) {
						peer = x
						break
					}
				}
				if peer == nil {
					if hint != "" {
						peer = hint
					} else {
						peer = "?"
					}
				}
				*out = append(*out, BGPSess{PyStr(unwrap(peer)), pyStripU(v)})
			}
			break
		}
	}
	for _, k := range m.Keys() {
		if v := m.V(k); isContainer(v) {
			jsonBGP(v, out, k)
		}
	}
}

var (
	tbPeerRE  = pyRe(`^\s*(\d+\.\d+\.\d+\.\d+|[0-9a-fA-F]{1,4}(?::[0-9a-fA-F]{0,4}){2,7})(?:\s|$)`)
	tbNextRE  = pyRe(`^\s*\d+\.\d+\.\d+\.\d+\s`)
	tbStateRE = pyRe(`\b(Idle|Active|Connect|OpenSent|OpenConfirm)\b`)
	tbAdminRE = pyRe(`\bAdmin`)
	tbDateRE  = pyRe(`\d+/\d+/\d+`)
	tbEndRE   = pyRe(`\d+/\d+/\d+|\s\d+\s*$`)
)

// textBGP reads sessions from a text summary (SR OS, IOS XR, FTD).
func textBGP(t string) []BGPSess {
	var out []BGPSess
	L := pySplitlines(t)
	for i, l := range L {
		m := tbPeerRE.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		nxt := ""
		if i+1 < len(L) && !tbNextRE.MatchString(L[i+1]) {
			nxt = L[i+1]
		}
		seg := l + " " + nxt
		st := tbStateRE.FindStringSubmatch(seg)
		if tbAdminRE.MatchString(seg) {
			continue
		}
		if st != nil {
			out = append(out, BGPSess{m[1], st[1]})
		} else if tbEndRE.MatchString(l) || tbDateRE.MatchString(seg) {
			out = append(out, BGPSess{m[1], "Established"})
		}
	}
	return out
}

func looksJSON(s string) bool {
	// Python: s[:1] in "{[" (true for an empty string as well)
	return s == "" || s[0] == '{' || s[0] == '['
}

// BGPSessions is every session in the outputs (JSON or text), duplicates dropped.
func BGPSessions(texts []string) []BGPSess {
	var sessions []BGPSess
	for _, t := range texts {
		s := pyStripU(t)
		if looksJSON(s) {
			if obj, err := pyjson.Loads([]byte(s)); err == nil {
				jsonBGP(obj, &sessions, "")
				continue
			}
		}
		sessions = append(sessions, textBGP(t)...)
	}
	seen := map[[2]string]bool{}
	var uniq []BGPSess
	for _, x := range sessions {
		k := [2]string{x.Peer, pyLower(x.State)}
		if seen[k] {
			continue
		}
		seen[k] = true
		uniq = append(uniq, x)
	}
	return uniq
}

// ParseBGP is (established, total, "peer state" of the others).
func ParseBGP(texts []string) (int, int, []string) {
	uniq := BGPSessions(texts)
	est := 0
	bad := []string{}
	for _, x := range uniq {
		if strings.HasPrefix(pyLower(x.State), "established") {
			est++
		} else {
			bad = append(bad, x.Peer+" "+x.State)
		}
	}
	return est, len(uniq), bad
}

// ------------------------------------------------------------------------------------- LLDP

// LLDPLink is the neighbor seen on a local port.
type LLDPLink struct{ Name, Port string }

// LLDPPairs is {local port: (neighbor name, neighbor port)} from any vendor's LLDP JSON, or lldpctl keyvalue text.
// ok is false when nothing was understood. keys keeps Python's insertion order.
func LLDPPairs(t string) (pairs map[string]LLDPLink, keys []string, ok bool) {
	pairs = map[string]LLDPLink{}
	s := pyStripU(t)
	if looksJSON(s) {
		obj, err := pyjson.Loads([]byte(s))
		if err == nil {
			lldpWalk(obj, pairs, &keys)
			if len(pairs) == 0 {
				lldpRows(obj, pairs, &keys)
			}
			return pairs, keys, true
		}
	}
	type acc struct{ name, port string }
	tmp := map[string]*acc{}
	var order []string
	for _, ln := range pySplitlines(s) {
		m := lldpKVRE.FindStringSubmatch(pyStripU(ln))
		if m == nil {
			continue
		}
		a := tmp[m[1]]
		if a == nil {
			a = &acc{}
			tmp[m[1]] = a
			order = append(order, m[1])
		}
		if m[2] == "chassis.name" {
			a.name = m[3]
		} else if a.port == "" || m[2] == "port.ifname" {
			a.port = m[3]
		}
	}
	for _, k := range order {
		if tmp[k].name != "" {
			pairs[k] = LLDPLink{tmp[k].name, tmp[k].port}
			keys = append(keys, k)
		}
	}
	return pairs, keys, len(keys) > 0
}

var (
	lldpName  = []string{"system-name", "system_name", "systemName", "lldp-remote-system-name", "sys_name", "chassis-name"}
	lldpPort  = []string{"port-id", "port_id", "portId", "lldp-remote-port-id", "ifname", "port-name", "port_description", "port-description", "description"}
	lldpPortD = []string{"ifname", "name", "id", "port-id"}
)

// lldpFind is find(o, keys): the first str/int (bool included) value under one of keys, depth first.
func lldpFind(o any, keys []string) string {
	switch x := o.(type) {
	case *pyjson.Map:
		for _, k := range keys {
			if x.Has(k) {
				switch u := unwrap(x.V(k)).(type) {
				case string, int64, bool:
					return PyStr(u)
				case float64:
					// a float is not an int in Python, but a JSON integer too large for int64 lands here
					if u == float64(int64(u)) && (u > 9.2e18 || u < -9.2e18) {
						return new(big.Float).SetFloat64(u).Text('f', 0)
					}
				}
			}
		}
		for _, k := range x.Keys() {
			if r := lldpFind(x.V(k), keys); r != "" {
				return r
			}
		}
	case []any:
		for _, v := range x {
			if r := lldpFind(v, keys); r != "" {
				return r
			}
		}
	}
	return ""
}

func lldpPFind(o any) (*pyjson.Map, bool) {
	switch x := o.(type) {
	case *pyjson.Map:
		for _, kk := range x.Keys() {
			vv := x.V(kk)
			if kk == "port" || kk == "port-info" || kk == "remote-port" {
				if m, ok := vv.(*pyjson.Map); ok {
					return m, true
				}
			}
			if r, ok := lldpPFind(vv); ok {
				return r, true
			}
		}
	case []any:
		for _, vv := range x {
			if r, ok := lldpPFind(vv); ok {
				return r, true
			}
		}
	}
	return nil, false
}

func lldpWalk(o any, out map[string]LLDPLink, keys *[]string) {
	switch x := o.(type) {
	case *pyjson.Map:
		for _, k := range x.Keys() {
			v := x.V(k)
			if ifKeyRE.MatchString(k) && isContainer(v) {
				nm := lldpFind(v, lldpName)
				if nm == "" {
					nm = lldpFind(v, []string{"name"})
				}
				pt := ""
				if pd, ok := lldpPFind(v); ok {
					pt = lldpFind(pd, lldpPortD)
				}
				if pt == "" {
					pt = lldpFind(v, lldpPort)
				}
				if nm != "" {
					if _, seen := out[k]; !seen {
						*keys = append(*keys, k)
					}
					out[k] = LLDPLink{nm, pt}
				}
			} else {
				lldpWalk(v, out, keys)
			}
		}
	case []any:
		for _, v := range x {
			lldpWalk(v, out, keys)
		}
	}
}

// vnum is the digits of a release joined by dots.
// vnum is the release's numbers, leading zeros dropped (IOS XE prints 17.15.06 for 17.15.6). HS v2.05
func vnum(v string) string {
	xs := asciiDigitsRE.FindAllString(v, -1)
	for i, x := range xs {
		if x = strings.TrimLeft(x, "0"); x == "" {
			x = "0"
		}
		xs[i] = x
	}
	return strings.Join(xs, ".")
}

func sameDev(seen, name, short string) bool {
	s := pyLower(strings.SplitN(seen, ".", 2)[0])
	return s != "" && (s == pyLower(name) || s == pyLower(short))
}

// ------------------------------------------------------------------------------------- configuration normalization

var (
	secretKWRE   = pyRe(`(?i)\b(secret|hashed-password|password|auth-sha|auth-md5|encrypt-aes|encrypt-des|readonly-community|readwrite-community|psk|pre-shared-key|encrypted-password)\s+('[^']*'|\x22[^\x22]*\x22|\S+)`)
	trailCommRE  = pyRe(`\s+#\s.*$`)
	nvsecRE      = pyRe(`\$nvsec\$\S+`)
	spacesRE     = pyRe(`\s+`)
	nvStaticRE   = pyRe(`^(nv set vrf \S+ router static \S+ via \S+) type ipv4-address$`)
	multiValRE   = pyRe(`^(.* )(\d+(?:,\d+)+)$`)
	nvIfRE       = pyRe(`^(nv set interface )(\S+)( .*)$`)
	digDashDigRE = pyRe(`\d-\d`)
	nvRangeRE    = pyRe(`^(.*?)(\d+)-(\d+)$`)
	nonDigitsRE  = pyRe(`^\D*`)
	nonDigits1RE = pyRe(`^\D+`)
)

// diffDefaults are what NVUE adds on its own (shown as a count, not as differences).
var diffDefaults = map[string]*regexp.Regexp{"cumulus": pyRe(
	`^nv set (system aaa (class|role) |system aaa user (cumulus|telemetry) |system api state |system config (auto-save|backup) |` +
		`system control-plane acl acl-default-|system ntp server \d\.cumulusnetworks\.pool\.ntp\.org |system docker vrf |` +
		`interface \S+ type (eth|swp|loopback)$|interface eth0 ipv4 dhcp-client set-hostname |system ssh-server state enabled$|` +
		`system lldp tlv egress-policy |router bgp state enabled$|system aaa user \S+ full-name |interface eth0 vrf mgmt$|qos roce state enabled$|system wjh state enabled$|vrf default router bgp state enabled$|` +
		`vrf \S+ router static \S+ address-family ipv4-unicast$)`)}

// nvExpand expands NVUE interface ranges: "swp1-24,33-56,swp1s0-1".
func nvExpand(lst string) []string {
	var out []string
	pre := ""
	lim := big.NewInt(4096)
	for _, t := range strings.Split(lst, ",") {
		m := nvRangeRE.FindStringSubmatch(t)
		if m != nil {
			a, _ := new(big.Int).SetString(pyDigitsASCII(m[2]), 10)
			b, _ := new(big.Int).SetString(pyDigitsASCII(m[3]), 10)
			if a != nil && b != nil && b.Cmp(a) >= 0 && new(big.Int).Sub(b, a).Cmp(lim) < 0 {
				p := pre
				if m[1] != "" {
					p = m[1]
					pre = nonDigitsRE.FindString(m[1])
				}
				for i := new(big.Int).Set(a); i.Cmp(b) <= 0; i.Add(i, big.NewInt(1)) {
					out = append(out, p+i.String())
				}
				continue
			}
		}
		if x := nonDigits1RE.FindString(t); x != "" {
			pre = x
		}
		out = append(out, t)
	}
	return out
}

// pyDigitsASCII maps Unicode decimal digits to ASCII (int() accepts them).
func pyDigitsASCII(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			ascii = false
		}
	}
	if ascii {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if r < 128 {
			b.WriteRune(r)
		} else {
			b.WriteByte(byte('0' + digitVal(r)))
		}
	}
	return b.String()
}

func replaceSubmatchFunc(re *regexp.Regexp, s string, f func(m []string) string) string {
	idx := re.FindAllStringSubmatchIndex(s, -1)
	if idx == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, ix := range idx {
		b.WriteString(s[last:ix[0]])
		m := make([]string, len(ix)/2)
		for g := 0; g < len(m); g++ {
			if ix[2*g] >= 0 {
				m[g] = s[ix[2*g]:ix[2*g+1]]
			}
		}
		b.WriteString(f(m))
		last = ix[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// StrSet is a set of strings.
type StrSet map[string]struct{}

func (s StrSet) add(x string) { s[x] = struct{}{} }

// Sorted is the set's members in Python sort order (code point order = UTF-8 byte order).
func (s StrSet) Sorted() []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CfgNorm puts a planned configuration and a backup in one form: no comments, quotes or blank lines; secrets masked;
// NVUE ranges expanded.
func CfgNorm(t, os string) StrSet {
	out := StrSet{}
	cfgNormInto(t, os, out)
	return out
}

func cfgNormInto(t, os string, out StrSet) {
	for _, l := range pySplitlines(t) {
		s := pyStripU(l)
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "!") {
			continue
		}
		s = trailCommRE.ReplaceAllString(s, "")
		s = replaceSubmatchFunc(secretKWRE, s, func(m []string) string { return m[1] + " <secret>" })
		s = nvsecRE.ReplaceAllString(s, "<secret>")
		s = strings.ReplaceAll(strings.ReplaceAll(s, "'", ""), "\"", "")
		s = pyStripU(spacesRE.ReplaceAllString(s, " "))
		if os == "cumulus" {
			if strings.HasPrefix(s, "nv ") && !strings.HasPrefix(s, "nv set ") && !strings.HasPrefix(s, "nv unset ") {
				continue
			}
			s = nvStaticRE.ReplaceAllString(s, "${1}")
			if mv := multiValRE.FindStringSubmatch(s); mv != nil {
				for _, v := range strings.Split(mv[2], ",") {
					cfgNormInto(mv[1]+v, os, out)
				}
				continue
			}
		}
		if os == "cumulus" {
			if m := nvIfRE.FindStringSubmatch(s); m != nil && (strings.Contains(m[2], ",") || digDashDigRE.MatchString(m[2])) {
				for _, x := range nvExpand(m[2]) {
					out.add(m[1] + x + m[3])
				}
				continue
			}
		}
		out.add(s)
	}
}

// ------------------------------------------------------------------------------------- lossless acceptance counters

// FlatNum is an ordered path -> number map (Python dict order).
type FlatNum struct {
	Keys []string
	M    map[string]float64
}

func newFlat() *FlatNum { return &FlatNum{M: map[string]float64{}} }

func (f *FlatNum) set(k string, v float64) {
	if _, ok := f.M[k]; !ok {
		f.Keys = append(f.Keys, k)
	}
	f.M[k] = v
}

// AccFlat flattens a JSON tree into path -> number (keys lower-cased; booleans and non-numbers left out).
func AccFlat(o any, pre string, out *FlatNum) *FlatNum {
	if out == nil {
		out = newFlat()
	}
	switch x := o.(type) {
	case *pyjson.Map:
		for _, k := range x.Keys() {
			AccFlat(x.V(k), pre+"/"+pyLower(k), out)
		}
	case []any:
		for i, v := range x {
			AccFlat(v, pre+"/"+itoa(i), out)
		}
	case bool, nil:
	case int64:
		out.set(pre, float64(x))
	case float64:
		out.set(pre, x)
	case string:
		if f, ok := pyFloat(x); ok {
			out.set(pre, f)
		}
	}
	return out
}

var (
	accTextLineRE = pyRe(`^\s*([A-Za-z][\w ./()-]*?)\s*[:=]\s*(-?\d+(?:\.\d+)?)\s*$`)
)

// AccFlatText is AccFlat of a JSON output, or "name: number" lines of a text one.
func AccFlatText(t string) *FlatNum {
	t = pyStripU(t)
	lt := pyLower(t)
	if t == "" || strings.HasPrefix(lt, "error") || strings.HasPrefix(lt, "%") || strings.HasPrefix(lt, "invalid") || strings.HasPrefix(lt, "syntax error") {
		return newFlat()
	}
	if obj, err := pyjson.Loads([]byte(t)); err == nil {
		return AccFlat(obj, "", nil)
	}
	out := newFlat()
	for _, l := range pySplitlines(t) {
		if m := accTextLineRE.FindStringSubmatch(l); m != nil {
			f, _ := pyFloat(m[2])
			out.set("/"+pyLower(pyStripU(m[1])), f)
		}
	}
	return out
}

// AccCount is PFC sent/received on priority 3, ECN marks and drops on the RoCE queue, PFC watchdog events.
type AccCount struct {
	PfcTx, PfcRx, Ecn, Drops, Wd float64
	Seen                         map[string]bool
}

func newAccCount() *AccCount { return &AccCount{Seen: map[string]bool{}} }

func (c *AccCount) get(k string) float64 {
	switch k {
	case "pfc_tx":
		return c.PfcTx
	case "pfc_rx":
		return c.PfcRx
	case "ecn":
		return c.Ecn
	case "drops":
		return c.Drops
	case "wd":
		return c.Wd
	}
	return 0
}

func (c *AccCount) add(k string, v float64) {
	switch k {
	case "pfc_tx":
		c.PfcTx += v
	case "pfc_rx":
		c.PfcRx += v
	case "ecn":
		c.Ecn += v
	case "drops":
		c.Drops += v
	case "wd":
		c.Wd += v
	}
}

var accKeys = []string{"pfc_tx", "pfc_rx", "ecn", "drops", "wd"}

var (
	accWdRE     = regexp.MustCompile(`watchdog|storm`)
	accWdSkipRE = regexp.MustCompile(`timer|interval|time|enable|state|config|action|polling|recovery`)
	accPfcRE    = regexp.MustCompile(`pfc|pause`)
	accTxRE     = regexp.MustCompile(`tx|sent|xmit|transmit|/out`)
	accRxRE     = regexp.MustCompile(`rx|recv|receiv|/in`)
	accEcnRE    = regexp.MustCompile(`ecn|marked|/ce(\b|/|$)`)
	accDropRE   = regexp.MustCompile(`drop|discard`)
)

// AccCounts classifies counters by key name.
func AccCounts(flat *FlatNum) *AccCount {
	c := newAccCount()
	for _, p := range flat.Keys {
		v := flat.M[p]
		q3 := accQ3RE.MatchString(p)
		if accWdRE.MatchString(p) {
			if !accWdSkipRE.MatchString(p) {
				c.Wd += v
				c.Seen["wd"] = true
			}
			continue
		}
		if accPfcRE.MatchString(p) && q3 {
			if accTxRE.MatchString(p) {
				c.PfcTx += v
				c.Seen["pfc"] = true
			} else if accRxRE.MatchString(p) {
				c.PfcRx += v
				c.Seen["pfc"] = true
			}
			continue
		}
		if accEcnRE.MatchString(p) && q3 {
			c.Ecn += v
			c.Seen["ecn"] = true
			continue
		}
		if accDropRE.MatchString(p) && q3 {
			c.Drops += v
			c.Seen["drops"] = true
		}
	}
	return c
}

var accKVRE = pyRe(`(\w+)=(\S+)`)

// AccParse reads the host script's lines for one test: ACCEPT <test> k=v ... and NIC <test> <counter> <delta>.
// kv values are float64 or string; both maps keep insertion order.
func AccParse(text, test string) (*pyjson.Map, *FlatNum) {
	kv, nic := pyjson.NewMap(), newFlat()
	acc := pyRe(`^ACCEPT\s+` + regexp.QuoteMeta(test) + `\s+(.*)$`)
	nicRE := pyRe(`^NIC\s+` + regexp.QuoteMeta(test) + `\s+(\S+)\s+(-?\d+(?:\.\d+)?)$`)
	for _, l := range pySplitlines(text) {
		ls := pyStripU(l)
		if m := acc.FindStringSubmatch(ls); m != nil {
			for _, p := range accKVRE.FindAllStringSubmatch(m[1], -1) {
				if f, ok := pyFloat(p[2]); ok {
					kv.Set(p[1], f)
				} else {
					kv.Set(p[1], p[2])
				}
			}
		}
		if m := nicRE.FindStringSubmatch(ls); m != nil {
			f, _ := pyFloat(m[2])
			nic.set(m[1], nic.M[m[1]]+f)
		}
	}
	return kv, nic
}

func itoa(i int) string { return pyD(float64(i)) }

var (
	lldpRowLocal  = []string{"lldp-local-port-id", "lldp-local-interface", "l_port_id", "port"}
	lldpRowName   = []string{"lldp-remote-system-name", "neighborDevice", "sys_name", "system-name"}
	lldpRowPort   = []string{"lldp-remote-port-id", "neighborPort", "port_id", "port-id", "lldp-remote-port-description"}
	lldpDigitsRE  = pyRe(`^[0-9]+$`)
	asciiDigitsRE = regexp.MustCompile(`[0-9]+`)
	lldpSpaceRE   = pyRe(`\s`)
)

// lldpFirst is lldp_first: the first non-empty str/int (not bool) under one of keys, one level down.
func lldpFirst(keys []string, d *pyjson.Map) string {
	for _, k := range keys {
		if !d.Has(k) {
			continue
		}
		v := ""
		switch u := unwrap(d.V(k)).(type) {
		case string:
			v = u
		case int64:
			v = PyStr(u)
		case float64:
			if u == float64(int64(u)) && (u > 9.2e18 || u < -9.2e18) {
				v = new(big.Float).SetFloat64(u).Text('f', 0)
			}
		}
		if v != "" {
			return v
		}
	}
	return ""
}

// lldpRows is lldp_rows: one row per neighbor (Junos, EOS, NX-OS), or SR Linux's interface list with its neighbors.
func lldpRows(o any, out map[string]LLDPLink, keys *[]string) {
	add := func(lp string, l LLDPLink) {
		if _, seen := out[lp]; !seen {
			out[lp] = l
			*keys = append(*keys, lp)
		}
	}
	switch x := o.(type) {
	case *pyjson.Map:
		lp, nm := lldpFirst(lldpRowLocal, x), lldpFirst(lldpRowName, x)
		if lp != "" && nm != "" {
			add(lp, LLDPLink{nm, lldpFirst(lldpRowPort, x)})
			return
		}
		name, isStr := x.V("name").(string)
		if nb, isList := x.V("neighbor").([]any); isStr && isList {
			for _, y := range nb {
				if m, ok := y.(*pyjson.Map); ok {
					if sn := lldpFirst([]string{"system-name"}, m); sn != "" {
						add(name, LLDPLink{sn, lldpFirst([]string{"port-id"}, m)})
					}
				}
			}
			return
		}
		for _, k := range x.Keys() {
			lldpRows(x.V(k), out, keys)
		}
	case []any:
		for _, v := range x {
			lldpRows(v, out, keys)
		}
	}
}

// LLDPPortNamed is lldp_port_named: false when the far end sends its port as an SNMP index or a description.
func LLDPPortNamed(p string) bool {
	p = pyStripU(p)
	return p != "" && !lldpDigitsRE.MatchString(p) && !lldpSpaceRE.MatchString(p)
}
