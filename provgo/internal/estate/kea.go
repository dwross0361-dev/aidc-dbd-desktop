package estate

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aidc-dbd/provgo/internal/pyjson"
)

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// pyJoin is os.path.join(a, b): an absolute b replaces a.
func pyJoin(a, b string) string {
	if strings.HasPrefix(b, "/") {
		return b
	}
	if a == "" || strings.HasSuffix(a, "/") {
		return a + b
	}
	return a + "/" + b
}

func (e *Estate) keepOption(o any) bool {
	m, ok := o.(*pyjson.Map)
	if !ok {
		return true
	}
	if m.Str("name") == "image-file-name" && !isFile(pyJoin(e.Conf.Str("web_root"), PyStr(dget(m, "data", "")))) {
		return false
	}
	return true
}

// keaEntry returns (reservation, client class) for a device, either may be nil.
func (e *Estate) keaEntry(n string) (*pyjson.Map, *pyjson.Map) {
	d := e.Devs[n]
	i, has := e.Ident.Get(n)
	k := d.M("dhcp")
	if k == nil || k.Len() == 0 || !has {
		return nil, nil
	}
	res := pyjson.O("ip-address", d.V("ip"), "hostname", n)
	for _, f := range []string{"boot-file-name", "next-server", "server-hostname"} {
		if k.Truthy(f) {
			res.Set(f, k.V(f))
		}
	}
	var opts []any
	for _, o := range k.L("options") {
		if e.keepOption(o) {
			opts = append(opts, o)
		}
	}
	forms := pyjson.StrList(k.V("sn_forms"))
	switch {
	case i.Kind == "mac":
		res.Set("hw-address", NormMAC(i.Value))
	case i.Kind == "sn" && containsStr(forms, "client-id"):
		var parts []string
		for _, c := range i.Value {
			parts = append(parts, fmt.Sprintf("%02x", c))
		}
		res.Set("client-id", "00:"+strings.Join(parts, ":"))
	case i.Kind == "sn" && containsStr(forms, "vendor-class"):
		pfx := "Juniper--"
		if v, ok := k.Get("vc_prefix"); ok {
			pfx = PyStr(v)
		}
		vc := pfx + i.Value
		var t string
		if KeaTextRE.MatchString(vc) {
			t = fmt.Sprintf("option[60].text == '%s'", vc)
		} else {
			t = "option[60].hex == 0x" + hex.EncodeToString([]byte(vc))
		}
		cls := pyjson.O("name", "dev-"+n, "test", t)
		if len(opts) > 0 {
			cls.Set("option-data", opts)
		}
		if res.Truthy("boot-file-name") {
			cls.Set("boot-file-name", res.V("boot-file-name"))
		}
		if res.Truthy("next-server") {
			cls.Set("next-server", res.V("next-server"))
		}
		return nil, cls
	default:
		return nil, nil
	}
	if len(opts) > 0 {
		res.Set("option-data", opts)
	}
	return res, nil
}

type keaNet struct {
	pfx netip.Prefix
	sub *pyjson.Map
}

func pyIPNetwork(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		a, err2 := netip.ParseAddr(s)
		if err2 != nil {
			return netip.Prefix{}, fmt.Errorf("'%s' does not appear to be an IPv4 or IPv6 network", s)
		}
		p = netip.PrefixFrom(a, a.BitLen())
	}
	if p.Masked() != p {
		return netip.Prefix{}, fmt.Errorf("%s has host bits set", s)
	}
	return p, nil
}

func appendList(m *pyjson.Map, key string, v any) {
	l, _ := m.SetDefault(key, []any{}).([]any)
	m.Set(key, append(l, v))
}

// RenderKea builds the Kea configuration: the template plus the open waves' reservations, classes and link subnets.
func (e *Estate) RenderKea() (cfg *pyjson.Map, count int, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	cfg = pyjson.DeepCopy(e.KeaTmpl).(*pyjson.Map)
	d4 := cfg.M("Dhcp4")
	if d4 == nil {
		return nil, 0, errors.New("'Dhcp4'")
	}
	sid := e.Plan.M("dhcp").V("subnet_id")
	var subs []*pyjson.Map
	for _, x := range d4.L("subnet4") {
		subs = append(subs, x.(*pyjson.Map))
	}
	for _, sn := range d4.L("shared-networks") {
		for _, x := range sn.(*pyjson.Map).L("subnet4") {
			subs = append(subs, x.(*pyjson.Map))
		}
	}
	found := false
	for _, x := range subs {
		if pyEq(x.V("id"), sid) {
			found = true
		}
	}
	if !found {
		return nil, 0, fmt.Errorf("kea-dhcp4.json has no subnet with id %s (plan dhcp.subnet_id)", PyStr(sid))
	}
	var nets []keaNet
	for _, x := range subs {
		p, err := pyIPNetwork(x.Str("subnet"))
		if err != nil {
			return nil, 0, err
		}
		nets = append(nets, keaNet{p, x})
	}
	subOf := func(ip string) *pyjson.Map {
		a, err := netip.ParseAddr(ip)
		if err != nil {
			panic(fmt.Sprintf("'%s' does not appear to be an IPv4 or IPv6 address", ip))
		}
		for _, nt := range nets {
			if nt.pfx.Contains(a) {
				return nt.sub
			}
		}
		return nil
	}
	var classes, links []any
	var tests []string
	used := map[string]bool{}
	for _, x := range subs {
		used[PyStr(x.V("id"))] = true
	}
	lid := map[[2]string]int64{}
	nx := int64(1000)
	for _, n := range e.DevOrder {
		for _, L := range e.Devs[n].L("ztp_links") {
			for used[fmt.Sprint(nx)] {
				nx++
			}
			lid[[2]string{n, L.(*pyjson.Map).Str("subnet")}] = nx
			used[fmt.Sprint(nx)] = true
		}
	}
	for _, n := range e.DevOrder {
		if !e.waveOpen(n) || e.flag(n) == "quarantined" {
			continue
		}
		r, c := e.keaEntry(n)
		d := e.Devs[n]
		for _, Lv := range d.L("ztp_links") {
			L := Lv.(*pyjson.Map)
			k := d.M("dhcp")
			o := []any{pyjson.O("name", "routers", "data", L.V("router"))}
			for _, x := range k.L("options") {
				if e.keepOption(x) {
					o = append(o, x)
				}
			}
			s4 := pyjson.O("id", lid[[2]string{n, L.Str("subnet")}], "subnet", L.V("subnet"),
				"pools", []any{pyjson.O("pool", fmt.Sprintf("%s - %s", L.Str("ip"), L.Str("ip")))}, "option-data", o)
			for _, f := range []string{"boot-file-name", "next-server", "server-hostname"} {
				if k.Truthy(f) {
					s4.Set(f, k.V(f))
				}
			}
			links = append(links, s4)
		}
		if r != nil {
			if sb := subOf(r.Str("ip-address")); sb != nil {
				appendList(sb, "reservations", r)
			}
		}
		seed := d.Str("seed_ip")
		if seed != "" && r != nil {
			if sb := subOf(seed); sb != nil {
				r2 := r.Copy()
				r2.Set("ip-address", seed)
				appendList(sb, "reservations", r2)
			}
		}
		if seed != "" && c != nil {
			if sb := subOf(seed); sb != nil {
				appendList(sb, "pools", pyjson.O("pool", fmt.Sprintf("%s - %s", seed, seed), "client-class", c.V("name")))
			}
		}
		if c != nil {
			classes = append(classes, c)
			tests = append(tests, c.Str("test"))
			if sb := subOf(d.Str("ip")); sb != nil {
				appendList(sb, "pools", pyjson.O("pool", fmt.Sprintf("%s - %s", d.Str("ip"), d.Str("ip")), "client-class", c.V("name")))
			}
		}
	}
	for _, cv := range d4.L("client-classes") {
		c := cv.(*pyjson.Map)
		if c.Str("name") == "PINNED" {
			if len(tests) > 0 {
				parts := make([]string, len(tests))
				for i, t := range tests {
					parts[i] = "(" + t + ")"
				}
				c.Set("test", strings.Join(parts, " or "))
			} else {
				c.Set("test", "'pinned' == 'none'")
			}
		}
	}
	cc := append(append([]any{}, classes...), d4.L("client-classes")...)
	d4.Set("client-classes", cc)
	if len(links) > 0 {
		d4.Set("subnet4", append(append([]any{}, d4.L("subnet4")...), links...))
	}
	cnt := 0
	for _, x := range subs {
		cnt += len(x.L("reservations"))
	}
	return cfg, cnt + len(classes) + len(links), nil
}

// keaApply writes the Kea config and reloads Kea. A render error is returned (the tick logs it as an engine error).
func (e *Estate) keaApply() (bool, error) {
	cfg, n, err := e.RenderKea()
	if err != nil {
		return false, err
	}
	live := e.Conf.Str("kea_live")
	tmp := live + ".estate-tmp"
	if err := os.WriteFile(tmp, []byte(pyjson.DumpsIndent(cfg, 2)), 0o644); err != nil {
		e.KeaStatus = fmt.Sprintf("could not write %s: %s", live, pyErrno(err))
		return false, nil
	}
	if err := os.Rename(tmp, live); err != nil {
		e.KeaStatus = fmt.Sprintf("could not write %s: %s", live, pyErrno(err))
		return false, nil
	}
	sock := ""
	if cs := cfg.M("Dhcp4").M("control-socket"); cs != nil {
		sock = PyStr(cs.V("socket-name"))
		if cs.V("socket-name") == nil {
			sock = ""
		}
	}
	ok := false
	if sock != "" {
		r, err := keaCommand(sock)
		if err != nil {
			e.KeaStatus = fmt.Sprintf("%d reservations written; Kea control socket unreachable (%s)", n, err.Error())
		} else {
			ok = pyEq(r.V("result"), int64(0))
			txt := "reloaded"
			if v, has := r.Get("text"); has {
				txt = PyStr(v)
			}
			e.KeaStatus = fmt.Sprintf("%d reservations live (Kea: %s)", n, txt)
		}
	} else {
		e.KeaStatus = fmt.Sprintf("%d reservations written; no control socket configured", n)
	}
	parts := strings.Split(e.KeaStatus, ";")
	last := parts[len(parts)-1]
	if !ok && e.keaWarned != last {
		e.keaWarned = last
		e.ev("Kea: "+e.KeaStatus, "warn", "")
	} else if ok {
		e.keaWarned = ""
	}
	return ok, nil
}

// pyOSErr wraps an error already rendered the way Python prints it.
type pyOSErr string

func (p pyOSErr) Error() string { return string(p) }

func keaCommand(sock string) (*pyjson.Map, error) {
	c, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		return nil, pyOSErr(pyErrno(err))
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte(pyjson.Dumps(pyjson.O("command", "config-reload")))); err != nil {
		return nil, pyOSErr(pyErrno(err))
	}
	var resp []byte
	buf := make([]byte, 65536)
	for {
		k, err := c.Read(buf)
		resp = append(resp, buf[:k]...)
		if k > 0 {
			if _, perr := pyjson.Loads(resp); perr == nil {
				break
			}
		}
		if err != nil {
			if os.IsTimeout(err) {
				return nil, pyOSErr("timed out")
			}
			break
		}
	}
	text := string(resp)
	if text == "" {
		text = "{}"
	}
	v, err := pyjson.Loads([]byte(text))
	if err != nil {
		return nil, pyOSErr(err.Error())
	}
	if l, isL := v.([]any); isL {
		if len(l) == 0 {
			return pyjson.NewMap(), nil
		}
		v = l[0]
	}
	m, isM := v.(*pyjson.Map)
	if !isM {
		return pyjson.NewMap(), nil
	}
	return m, nil
}

var _ = filepath.Join
