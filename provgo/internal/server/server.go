// Package server holds the three HTTP listeners of the provisioning service: boot (web root and device check-ins),
// internal (Kea hook and CLI, 127.0.0.1 only) and the Deployment Console.
package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"aidc-dbd/provgo/internal/estate"
	"aidc-dbd/provgo/internal/pyjson"
)

// ------------------------------------------------------------------------------------------------ request helpers

// pyUnquote is urllib.parse.unquote: %XX decoded as UTF-8 (invalid bytes replaced), bad escapes kept.
func pyUnquote(s string, plus bool) string {
	if plus {
		s = strings.ReplaceAll(s, "+", " ")
	}
	if !strings.Contains(s, "%") {
		return s
	}
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s)+0 && i+2 <= len(s)-1 && isHex(s[i+1]) && isHex(s[i+2]) {
			v, _ := strconv.ParseUint(s[i+1:i+3], 16, 8)
			b = append(b, byte(v))
			i += 2
			continue
		}
		b = append(b, s[i])
	}
	return strings.ToValidUTF8(string(b), "�")
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// splitTarget is urlsplit(target) -> (path, query) for an origin-form or absolute-form request target.
func splitTarget(t string) (string, string) {
	if strings.HasPrefix(t, "//") { // http.server (3.12+) collapses a leading '//'
		t = "/" + strings.TrimLeft(t, "/")
	}
	if i := strings.Index(t, "#"); i >= 0 {
		t = t[:i]
	}
	if i := strings.Index(t, "://"); i > 0 && !strings.Contains(t[:i], "/") {
		rest := t[i+3:]
		if j := strings.IndexAny(rest, "/?"); j >= 0 {
			t = rest[j:]
		} else {
			t = ""
		}
	}
	q := ""
	if i := strings.Index(t, "?"); i >= 0 {
		t, q = t[:i], t[i+1:]
	}
	return t, q
}

// parseQS is urllib.parse.parse_qs (blank values dropped).
func parseQS(q string) map[string][]string {
	out := map[string][]string{}
	for _, p := range strings.Split(q, "&") {
		if p == "" {
			continue
		}
		i := strings.Index(p, "=")
		if i < 0 {
			continue
		}
		k, v := pyUnquote(p[:i], true), pyUnquote(p[i+1:], true)
		if v == "" {
			continue
		}
		out[k] = append(out[k], v)
	}
	return out
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// guard turns a panic into a dropped connection, as an exception in the Python handler does.
func guard(name string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if x := recover(); x != nil {
				if x == http.ErrAbortHandler {
					panic(x)
				}
				fmt.Fprintf(os.Stderr, "%s: %v\n%s", name, x, debug.Stack())
				panic(http.ErrAbortHandler)
			}
		}()
		h(w, r)
	}
}

func unsupported(w http.ResponseWriter, r *http.Request) {
	body := fmt.Sprintf("<!DOCTYPE HTML>\n<html lang=\"en\">\n    <head>\n        <meta charset=\"utf-8\">\n        <title>Error response</title>\n    </head>\n    <body>\n        <h1>Error response</h1>\n        <p>Error code: 501</p>\n        <p>Message: Unsupported method (%s).</p>\n        <p>Error code explanation: 501 - Server does not support this operation.</p>\n    </body>\n</html>\n", pyStrRepr(r.Method))
	w.Header().Set("Content-Type", "text/html;charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Connection", "close")
	w.WriteHeader(501)
	if r.Method != "HEAD" {
		io.WriteString(w, body)
	}
}

func pyStrRepr(s string) string { return estate.PyRepr(s) }

// ------------------------------------------------------------------------------------------------ boot listener

// BootHandler serves the web root (each file only to its device) and device check-ins.
func BootHandler(est *estate.Estate) http.Handler {
	root, err := filepath.EvalSymlinks(est.Conf.Str("web_root"))
	if err != nil {
		root, _ = filepath.Abs(est.Conf.Str("web_root"))
	}
	txt := func(w http.ResponseWriter, r *http.Request, code int, msg string) {
		b := msg + "\n"
		w.Header().Set("Server", "estate")
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.WriteHeader(code)
		if r.Method != "HEAD" {
			io.WriteString(w, b)
		}
	}
	return guard("boot", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			unsupported(w, r)
			return
		}
		ip := clientIP(r)
		rawPath, rawQ := splitTarget(r.RequestURI)
		path := pyUnquote(rawPath, false)
		q := parseQS(rawQ)
		if strings.HasPrefix(path, "/ci/") {
			parts := strings.Split(path, "/")
			if len(parts) < 4 {
				txt(w, r, 400, "/ci/<host>/<stage>")
				return
			}
			pmsg := ""
			if len(parts) > 4 {
				pmsg = strings.Join(parts[4:], "/")
			}
			msg := pmsg
			if v := q["msg"]; len(v) > 0 {
				msg = v[0]
			} else if v := q["n"]; len(v) > 0 {
				msg = v[0]
			}
			code, m := est.Checkin(parts[2], parts[3], msg, ip)
			txt(w, r, code, m)
			return
		}
		rel, ok := estate.CanonRel(path)
		if !ok {
			est.WithLock(func() {
				n := est.DevByIP(ip)
				p := path
				if r := []rune(p); len(r) > 120 {
					p = string(r[:120])
				}
				who := n
				if who == "" {
					who = ip
				}
				est.EventL(fmt.Sprintf("refused %s to %s: not a canonical file path", p, who), "warn", n)
			})
			txt(w, r, 400, "bad path")
			return
		}
		if strings.HasPrefix(rel, "licenses/") {
			var f string
			refused := false
			est.WithLock(func() {
				n := est.DevByIP(ip)
				base := filepath.Base(rel)
				ser := strings.ToUpper(strings.TrimSuffix(base, pyExt(base)))
				i, has := est.Ident.Get(n)
				f = est.LicenseFile(ser)
				if !(n != "" && has && i.Kind == "sn" && strings.ToUpper(i.Value) == ser && f != "") {
					who := n
					if who == "" {
						who = ip
					}
					est.EventL(fmt.Sprintf("refused %s to %s: license belongs to another serial", rel, who), "warn", n)
					refused = true
				}
			})
			if refused {
				txt(w, r, 403, "license belongs to another serial")
				return
			}
			body, err := os.ReadFile(f)
			if err != nil {
				panic(err)
			}
			w.Header()["Content-Type"] = nil
			w.Header().Set("Server", "estate")
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(200)
			w.Write(body)
			return
		}
		full, err := filepath.EvalSymlinks(filepath.Join(root, rel))
		st, serr := os.Stat(full)
		if err != nil || !strings.HasPrefix(full, root+string(os.PathSeparator)) || serr != nil || !st.Mode().IsRegular() {
			est.WithLock(func() {
				n := est.DevByIP(ip)
				if n != "" && strings.HasPrefix(rel, "images/") {
					est.EventL(fmt.Sprintf("%s asked for %s: not served on this host (the device decides by its baseline)", n, rel), "warn", n)
				} else if n != "" {
					est.EventL(fmt.Sprintf("%s asked for %s: not served (missing on this host)", n, rel), "fail", n)
					if est.FileOwner[rel] == n {
						est.FailL(n, rel+" is missing on the provisioning host")
					}
				}
			})
			txt(w, r, 404, "not found")
			return
		}
		var n string
		code, cmsg := 0, ""
		est.WithLock(func() {
			n = est.DevByIP(ip)
			owner := est.FileOwner[rel]
			local := ip == "127.0.0.1" || ip == "::1" || ip == est.Plan.M("site").Str("prov_ip")
			strict := pyjson.Truthy(est.Conf.V("strict_fetch"))
			if strict && owner != "" && !local && owner != n {
				who := n
				if who == "" {
					who = ip
				}
				est.EventL(fmt.Sprintf("refused %s to %s: it belongs to %s", rel, who, owner), "warn", owner)
				code, cmsg = 403, "this file belongs to another device"
				return
			}
			if strict && n == "" && !local {
				est.EventL(fmt.Sprintf("refused %s to %s: address is not a device of %s", rel, ip, est.Plan.M("site").Str("code")), "warn", "")
				code, cmsg = 403, "unknown requester"
				return
			}
			if toks := est.FileHasTokens(rel, full); len(toks) > 0 {
				est.EventL(fmt.Sprintf("refused %s: unfilled secret <%s> (run setup.sh to render secrets.env)", rel, toks[0]), "fail", n)
				if n != "" {
					est.FailL(n, fmt.Sprintf("%s still carries <%s>", rel, toks[0]))
				}
				code, cmsg = 409, "unfilled token"
			}
		})
		if code != 0 {
			txt(w, r, code, cmsg)
			return
		}
		if n != "" {
			if ov := est.ServedOverride(n, rel); ov != nil {
				w.Header().Set("Server", "estate")
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("Content-Length", strconv.Itoa(len(ov)))
				w.WriteHeader(200)
				if r.Method == "GET" {
					w.Write(ov)
					est.OnFetch(n, rel, int64(len(ov)), true)
				}
				return
			}
		}
		size := st.Size()
		ct := "text/plain"
		if strings.HasPrefix(rel, "images/") {
			ct = "application/octet-stream"
		}
		w.Header().Set("Server", "estate")
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		w.WriteHeader(200)
		ok = true
		if r.Method == "GET" {
			f, err := os.Open(full)
			if err != nil {
				ok = false
			} else {
				if _, err := io.Copy(w, f); err != nil {
					ok = false
				}
				f.Close()
			}
		}
		if n != "" && r.Method == "GET" {
			est.OnFetch(n, rel, size, ok)
		}
	})
}

// pyExt is os.path.splitext's extension of a base name.
func pyExt(base string) string {
	b := strings.TrimLeft(base, ".")
	i := strings.LastIndex(b, ".")
	if i < 0 {
		return ""
	}
	return b[i:]
}

// ------------------------------------------------------------------------------------------------ internal listener

func sendJSON(w http.ResponseWriter, code int, v any) {
	b := pyjson.Dumps(v)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(code)
	io.WriteString(w, b)
}

// readBody is read_body: nil for a negative, malformed or oversized Content-Length.
func readBody(r *http.Request, limit int64) ([]byte, bool) {
	cl := r.Header.Get("Content-Length")
	if cl == "" {
		cl = "0"
	}
	n, err := strconv.ParseInt(strings.TrimSpace(cl), 10, 64)
	if err != nil || n < 0 || n > limit {
		return nil, false
	}
	if n == 0 {
		return []byte{}, true
	}
	b := make([]byte, n)
	k, _ := io.ReadFull(r.Body, b)
	return b[:k], true
}

func notImpl(phase int, what string) *pyjson.Map {
	return pyjson.O("ok", false, "error", fmt.Sprintf("not implemented in this engine (phase %d)", phase),
		"msg", fmt.Sprintf("%s is not available in this engine yet (phase %d)", what, phase))
}

func jsonData(raw []byte) *pyjson.Map {
	if len(raw) == 0 {
		return pyjson.NewMap()
	}
	m, err := pyjson.LoadsMap(raw)
	if err != nil {
		return nil
	}
	return m
}

func dstr(d *pyjson.Map, k, def string) string {
	v, ok := d.Get(k)
	if !ok {
		return def
	}
	return estate.PyStr(v)
}

func dval(d *pyjson.Map, k string, def any) any {
	v, ok := d.Get(k)
	if !ok {
		return def
	}
	return v
}

// LocalToken reads (and with create, makes) the root-only token for /local/*.
func LocalToken(conf *pyjson.Map, create bool) string {
	p := conf.Str("local_token_file")
	if create {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			if f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err == nil {
				f.WriteString(randHex(24))
				f.Close()
			}
		}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// InternalHandler is the 127.0.0.1 listener: Kea's lease hook and the CLI.
func InternalHandler(est *estate.Estate) http.Handler {
	token := ""
	if b, err := os.ReadFile(est.Conf.Str("hook_token_file")); err == nil {
		token = strings.TrimSpace(string(b))
	}
	ltoken := LocalToken(est.Conf, true)
	return guard("internal", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			p := r.RequestURI
			if strings.HasPrefix(p, "/local/handover") {
				sendJSON(w, 200, est.Handover())
				return
			}
			if strings.HasPrefix(p, "/local/status") {
				sendJSON(w, 200, est.LocalStatus())
				return
			}
			sendJSON(w, 404, pyjson.NewMap())
			return
		}
		if r.Method != "POST" {
			unsupported(w, r)
			return
		}
		path, rawQ := splitTarget(r.RequestURI)
		need := ltoken
		if path == "/hook/lease" {
			need = token
		}
		if need == "" || !hmac.Equal([]byte(r.Header.Get("X-Estate-Token")), []byte(need)) {
			sendJSON(w, 403, pyjson.O("error", "token"))
			return
		}
		body, ok := readBody(r, 1<<20)
		if !ok {
			sendJSON(w, 413, pyjson.O("error", "length"))
			return
		}
		q := parseQS(rawQ)
		if path == "/hook/lease" {
			ev := ""
			if v := q["event"]; len(v) > 0 {
				ev = v[0]
			}
			for _, e := range estate.ParseHook(body, ev) {
				est.Evidence(e, "kea")
			}
			sendJSON(w, 200, pyjson.O("ok", true))
			return
		}
		data := jsonData(body)
		if data == nil {
			data = pyjson.NewMap()
		}
		switch path {
		case "/local/start":
			ok, msg := est.StartRun(dstr(data, "plane", "all"), "cli:"+dstr(data, "user", "root"), dstr(data, "reason", ""))
			sendJSON(w, okCode(ok), pyjson.O("ok", ok, "msg", msg))
		case "/local/lab-slice":
			var macs *pyjson.Map
			if m, isM := data.V("macs").(*pyjson.Map); isM {
				macs = m
			}
			ok, msg := est.LabSlice(pyjson.StrList(data.V("keep")), dstr(data, "reason", ""), "cli:root", macs)
			sendJSON(w, okCode(ok), pyjson.O("ok", ok, "msg", msg))
		case "/local/pause", "/local/resume":
			est.LocalPauseResume(strings.HasSuffix(path, "pause"))
			sendJSON(w, 200, pyjson.O("ok", true))
		case "/local/result":
			var kind any = "nccl"
			if v, ok := data.Get("kind"); ok {
				kind = v
			}
			ks, isS := kind.(string)
			if !isS {
				panic(http.ErrAbortHandler) // Python: TypeError in "result-" + kind, connection dropped
			}
			ok, msg := est.SubmitResult(ks, dval(data, "busbw", int64(0)), dval(data, "target", int64(0)), "cli", "")
			sendJSON(w, 200, pyjson.O("ok", ok, "msg", msg))
		case "/local/release-vendor":
			ok, msg := est.ReleaseVendor("cli:root", estate.PyStr(dval(data, "password", "")), data, true)
			sendJSON(w, okCode(ok), pyjson.O("ok", ok, "msg", msg))
		case "/local/handover-export":
			out := ""
			if v := data.V("out"); pyjson.Truthy(v) {
				out = estate.PyStr(v)
			}
			p, err := est.HandoverExportLocal(out)
			if err != nil {
				panic(http.ErrAbortHandler) // Python: the OSError ends the request without a reply
			}
			sendJSON(w, 200, pyjson.O("ok", true, "msg", p))
		case "/local/promote":
			ok, msg := est.Promote()
			sendJSON(w, okCode(ok), pyjson.O("ok", ok, "msg", msg))
		default:
			sendJSON(w, 404, pyjson.NewMap())
		}
	})
}

func okCode(ok bool) int {
	if ok {
		return 200
	}
	return 409
}

// ------------------------------------------------------------------------------------------------ console listener

var (
	cookieRE  = regexp.MustCompile(`estate_session=([A-Za-z0-9_-]+)`)
	backupRE  = regexp.MustCompile(`^/api/device/(.+)/(diff|backup)(?:/([0-9TZ]+\.cfg))?$`)
	userNmRE  = regexp.MustCompile(`^[A-Za-z0-9._@-]{2,40}$`)
	keySafeRE = regexp.MustCompile(`[^A-Za-z0-9_-]`)
)

const csp = "default-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self' data:; script-src 'self' 'unsafe-inline'; img-src 'self' data:"

// ConsoleHandler is the Deployment Console listener.
func ConsoleHandler(est *estate.Estate, html []byte, isTLS bool) http.Handler {
	var fmu sync.Mutex
	fails := map[string][]float64{}
	send := func(w http.ResponseWriter, code int, body any, ctype string, extra map[string]string) {
		var b []byte
		switch x := body.(type) {
		case []byte:
			b = x
		case string:
			b = []byte(x)
		default:
			b = []byte(pyjson.Dumps(x))
		}
		if ctype == "" {
			ctype = "application/json"
		}
		h := w.Header()
		h.Set("Server", "estate")
		h.Set("Content-Type", ctype)
		h.Set("Content-Length", strconv.Itoa(len(b)))
		h.Set("Cache-Control", "no-store")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", csp)
		for k, v := range extra {
			h.Set(k, v)
		}
		w.WriteHeader(code)
		w.Write(b)
	}
	j := func(w http.ResponseWriter, code int, body any) { send(w, code, body, "", nil) }
	who := func(r *http.Request) *estate.Who {
		m := cookieRE.FindStringSubmatch(r.Header.Get("Cookie"))
		if m == nil {
			return nil
		}
		return est.Session(m[1])
	}
	get := func(w http.ResponseWriter, r *http.Request) {
		path, rawQ := splitTarget(r.RequestURI)
		if path == "/" || path == "/index.html" {
			send(w, 200, html, "text/html; charset=utf-8", nil)
			return
		}
		if path == "/inventory" {
			tok := est.Secrets()["INVENTORY_TOKEN"]
			if tok == "" || !hmac.Equal([]byte(r.Header.Get("X-Estate-Inventory")), []byte(tok)) {
				j(w, 403, pyjson.O("error", "inventory token"))
				return
			}
			j(w, 200, est.Inventory())
			return
		}
		u := who(r)
		if u == nil {
			j(w, 401, pyjson.O("error", "login"))
			return
		}
		q := parseQS(rawQ)
		qv := func(k, def string) string {
			if v := q[k]; len(v) > 0 {
				return v[0]
			}
			return def
		}
		if path == "/api/whoami" {
			j(w, 200, u.Map())
			return
		}
		if u.MustChange {
			j(w, 403, pyjson.O("error", "password change required"))
			return
		}
		switch {
		case path == "/api/users":
			if u.Role != "admin" {
				j(w, 403, pyjson.O("error", "admins only"))
				return
			}
			j(w, 200, est.UsersView())
			return
		case path == "/api/plan":
			j(w, 200, est.PlanView())
			return
		case path == "/api/state":
			j(w, 200, est.StateView())
			return
		case path == "/api/events":
			since, err1 := strconv.ParseInt(strings.TrimSpace(qv("since", "0")), 10, 64)
			lim, err2 := strconv.ParseInt(strings.TrimSpace(qv("limit", "500")), 10, 64)
			if err1 != nil || err2 != nil {
				panic(http.ErrAbortHandler) // Python: ValueError, connection dropped
			}
			if lim > 200000 {
				lim = 200000
			}
			j(w, 200, est.Events(since, lim))
			return
		}
		if m := backupRE.FindStringSubmatch(path); m != nil {
			n := pyUnquote(m[1], false)
			if !est.HasDevice(n) {
				j(w, 404, pyjson.O("error", "unknown device"))
				return
			}
			if m[2] == "diff" {
				j(w, 200, est.Diff(n, qv("vs", "")))
				return
			}
			t, ok := est.BackupFile(n, m[3])
			if !ok {
				j(w, 404, pyjson.O("error", "no such backup"))
				return
			}
			send(w, 200, t, "text/plain", map[string]string{"Content-Disposition": fmt.Sprintf("attachment; filename=%s-%s", n, m[3])})
			return
		}
		switch {
		case path == "/api/gate-evidence":
			j(w, 200, est.GateEvidence(qv("build", ""), qv("check", "")))
		case strings.HasPrefix(path, "/api/runner-log/"):
			key := keySafeRE.ReplaceAllString(path[strings.LastIndex(path, "/")+1:], "")
			send(w, 200, est.Tail("runner-"+key+".log"), "text/plain", nil)
		case strings.HasPrefix(path, "/api/device/"):
			n := pyUnquote(path[len("/api/device/"):], false)
			if !est.HasDevice(n) {
				j(w, 404, pyjson.O("error", "unknown device"))
				return
			}
			j(w, 200, est.DeviceView(n))
		case path == "/api/handover/export.zip":
			if u.Role != "admin" || u.Org != "customer" {
				j(w, 403, pyjson.O("ok", false, "msg", "the hand-over export is downloaded by a customer admin"))
				return
			}
			fn, body := est.HandoverZip(u.User)
			sum := sha256.Sum256(body)
			send(w, 200, body, "application/zip", map[string]string{"Content-Disposition": "attachment; filename=" + fn,
				"X-SHA256": hex.EncodeToString(sum[:])})
		case path == "/api/readiness":
			j(w, 200, est.Readiness())
		case strings.HasPrefix(path, "/api/export/"):
			name := path[strings.LastIndex(path, "/")+1:]
			body, ok := est.Export(name)
			if !ok {
				j(w, 404, pyjson.O("error", "unknown export"))
				return
			}
			send(w, 200, body, "text/csv", map[string]string{"Content-Disposition": fmt.Sprintf("attachment; filename=%s-%s", est.Plan.M("site").Str("code"), name)})
		default:
			j(w, 404, pyjson.O("error", "not found"))
		}
	}
	post := func(w http.ResponseWriter, r *http.Request) {
		path, _ := splitTarget(r.RequestURI)
		limit := int64(1 << 20)
		if path == "/sync/upload" {
			tok := est.SyncToken()
			if est.RoleNow() != "standby" || tok == "" || !hmac.Equal([]byte(r.Header.Get("X-Estate-Sync")), []byte(tok)) {
				j(w, 403, pyjson.O("error", "sync"))
				return
			}
			limit = 512 << 20
			// a snapshot may take longer than the 30 s request limit to arrive
			_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(15 * time.Minute))
		}
		raw, ok := readBody(r, limit)
		if !ok {
			j(w, 413, pyjson.O("error", "length"))
			return
		}
		if path == "/sync/upload" {
			if err := est.ReceiveSnapshot(raw); err != nil {
				fmt.Fprintf(os.Stderr, "sync/upload: %v\n", err)
				panic(http.ErrAbortHandler) // Python: the exception ends the request without a reply
			}
			j(w, 200, pyjson.O("ok", true))
			return
		}
		data := pyjson.NewMap()
		if len(raw) > 0 {
			v, err := pyjson.Loads(raw)
			if err != nil {
				j(w, 400, pyjson.O("error", "json"))
				return
			}
			m, isM := v.(*pyjson.Map)
			if !isM {
				panic(http.ErrAbortHandler)
			}
			data = m
		}
		ip := clientIP(r)
		if path == "/api/login" {
			fmu.Lock()
			var f []float64
			now := estate.Now()
			for _, t := range fails[ip] {
				if t > now-300 {
					f = append(f, t)
				}
			}
			fmu.Unlock()
			if len(f) >= 5 {
				j(w, 429, pyjson.O("error", "too many attempts; wait five minutes"))
				return
			}
			user := dstr(data, "user", "")
			tok := est.Login(user, dstr(data, "password", ""))
			if tok == "" {
				fmu.Lock()
				fails[ip] = append(f, estate.Now())
				fmu.Unlock()
				est.Audit(user, "login-failed", ip, "")
				j(w, 401, pyjson.O("error", "wrong user or password"))
				return
			}
			est.Audit(estate.PyStr(data.V("user")), "login", ip, "")
			sec := ""
			if isTLS {
				sec = " Secure;"
			}
			send(w, 200, pyjson.O("ok", true), "", map[string]string{"Set-Cookie": fmt.Sprintf("estate_session=%s; Path=/; HttpOnly;%s SameSite=Strict; Max-Age=43200", tok, sec)})
			return
		}
		u := who(r)
		if u == nil {
			j(w, 401, pyjson.O("error", "login"))
			return
		}
		if r.Header.Get("X-Requested-With") != "estate-console" {
			j(w, 403, pyjson.O("error", "csrf"))
			return
		}
		if path == "/api/logout" {
			est.Logout(u.Token)
			send(w, 200, pyjson.O("ok", true), "", map[string]string{"Set-Cookie": "estate_session=; Path=/; Max-Age=0"})
			return
		}
		if path == "/api/password" {
			ok, msg := est.ChangePassword(u.User, dstr(data, "current", ""), dstr(data, "new", ""), u.Token)
			j(w, okCode(ok), pyjson.O("ok", ok, "msg", msg))
			return
		}
		if u.MustChange {
			j(w, 403, pyjson.O("error", "password change required"))
			return
		}
		if u.Role == "viewer" {
			j(w, 403, pyjson.O("error", "read-only login"))
			return
		}
		if est.Role == "standby" && path != "/api/promote" {
			j(w, 409, pyjson.O("ok", false, "msg", "this is the standby provisioning host: act on the primary, or promote this one"))
			return
		}
		user := u.User
		n := dstr(data, "device", "")
		if !data.Has("device") || !pyjson.Truthy(data.V("device")) {
			n = ""
		}
		if n != "" && !est.HasDevice(n) {
			j(w, 404, pyjson.O("error", "unknown device"))
			return
		}
		p := path
		var need *string
		s := func(x string) *string { return &x }
		switch {
		case strings.HasPrefix(p, "/api/users/") || p == "/api/push-credential" || p == "/api/promote":
			need = nil
		case n != "":
			need = s(est.BuildOf(n))
		case p == "/api/gate/confirm" || p == "/api/gate/override" || p == "/api/gate/recheck":
			need = s(dstr(data, "build", ""))
		case p == "/api/run":
			need = s(dstr(data, "plane", "all"))
		case p == "/api/result" && dstr(data, "kind", "") == "isolation":
			need = s("fe")
		case p == "/api/result" || p == "/api/tuning/propose" || p == "/api/tuning/apply" || p == "/api/tuning/rollout" || p == "/api/tuning/profile":
			need = s("be")
		case p == "/api/tuning/approve":
			need = nil
		default:
			need = s("any")
		}
		if u.Role != "admin" {
			allowed := need != nil && *need != "" && (*need == "any" || est.MayAct(u, *need))
			if !allowed {
				pl := strings.Join(u.Planes, ", ")
				if pl == "" {
					pl = "no plane"
				}
				j(w, 403, pyjson.O("ok", false, "msg", "not permitted: your login acts on "+pl+" only"))
				return
			}
		}
		var okv bool
		var msg string
		switch p {
		case "/api/users/add":
			nm := pyStripS(dstr(data, "name", ""))
			if !userNmRE.MatchString(nm) {
				j(w, 409, pyjson.O("ok", false, "msg", "name: 2-40 letters, digits, . _ @ -"))
				return
			}
			if est.UserExists(nm) {
				j(w, 409, pyjson.O("ok", false, "msg", "that login exists: reset or update it instead"))
				return
			}
			pw, err := est.UserAdd(nm, dval(data, "role", "operator"), "", dval(data, "planes", ""), true, dstr(data, "org", "customer"))
			if err != nil {
				j(w, 409, pyjson.O("ok", false, "msg", err.Error()))
				return
			}
			est.Audit(user, "user-add", estate.PyStr(data.V("name")), fmt.Sprintf("role=%s planes=%s", estate.PyStr(data.V("role")), estate.PyStr(dval(data, "planes", ""))))
			j(w, 200, pyjson.O("ok", true, "msg", "created", "password", pw))
			return
		case "/api/users/reset":
			ok, res := est.UserReset(user, dstr(data, "name", ""))
			if ok {
				j(w, 200, pyjson.O("ok", true, "msg", "temporary password issued", "password", res))
			} else {
				j(w, 409, pyjson.O("ok", false, "msg", res, "password", nil))
			}
			return
		case "/api/users/update":
			okv, msg = est.UserUpdate(user, dstr(data, "name", ""), data.V("role"), data.V("planes"), data.V("disabled"), data.V("org"))
		case "/api/run":
			rd := est.Readiness()
			if !rd.Truthy("ok") && est.RunState() == "idle" {
				j(w, 409, pyjson.O("ok", false, "readiness", true, "msg", fmt.Sprintf("Readiness: %d item(s) fail or wait for confirmation: open Readiness…", rd.Int("fails"))))
				return
			}
			okv, msg = est.StartRun(dstr(data, "plane", "all"), user, dstr(data, "reason", ""))
		case "/api/readiness/confirm":
			note := dstr(data, "note", "")
			if pyStripS(note) != "" {
				if r := []rune(note); len(r) > 300 {
					note = string(r[:300])
				}
				okv, msg = est.ReadinessConfirm(user, dstr(data, "key", ""), note)
			} else {
				okv, msg = false, "a note or reason"
			}
		case "/api/pause":
			okv, msg = est.Pause(user)
			if !okv {
				j(w, 409, pyjson.O("ok", false, "msg", msg))
				return
			}
		case "/api/bind":
			okv, msg = est.Bind(dstr(data, "key", ""), n, user)
		case "/api/dismiss":
			okv, msg = est.Dismiss(dstr(data, "key", ""), user, dval(data, "reason", ""))
		case "/api/unbind":
			okv, msg = est.Unbind(n, user)
		case "/api/retry":
			okv, msg = est.Retry(n, user, false)
		case "/api/reserve":
			okv, msg = est.Retry(n, user, true)
		case "/api/quarantine":
			okv, msg = est.Quarantine(n, user, dstr(data, "reason", ""))
		case "/api/mark-configured":
			okv, msg = est.MarkConfigured(n, user, dstr(data, "note", ""))
		case "/api/count":
			okv, msg = est.Count(n, dval(data, "n", ""))
		case "/api/gate/confirm":
			okv, msg = est.GateConfirm(dstr(data, "build", ""), dstr(data, "check", ""), user, dstr(data, "note", ""))
		case "/api/gate/override":
			okv, msg = est.GateOverride(dstr(data, "build", ""), user, dstr(data, "reason", ""))
		case "/api/gate/recheck":
			okv, msg = est.GateRecheck(dstr(data, "build", ""), user)
		case "/api/backup":
			if n == "" {
				panic(http.ErrAbortHandler) // Python: KeyError on devs[""], connection dropped
			}
			res := est.TakeBackup(n, "requested by "+user)
			if res == "" {
				j(w, 409, pyjson.O("ok", false, "msg", "backup not possible (reader unavailable or unsupported)"))
			} else {
				// Python answers with the backup's description in "ok" (a string, not true)
				j(w, 200, pyjson.O("ok", res, "msg", fmt.Sprintf("Backed up %s: %s", time.Now().UTC().Format("15:04Z"), res)))
			}
			return
		case "/api/result":
			kind := dval(data, "kind", "nccl")
			switch {
			case kind == "isolation":
				okv, msg = est.SubmitIsolationText(pyHeadS(estate.PyStr(dval(data, "text", "")), 2000000), user)
			case kind == "accept":
				var crit *pyjson.Map
				if acc := est.Plan.M("acceptance"); acc != nil && acc.Truthy("criteria") {
					crit, _ = acc.V("criteria").(*pyjson.Map)
				}
				okv, msg = est.SubmitAcceptText(pyHeadS(estate.PyStr(dval(data, "text", "")), 2000000), user, crit)
			default:
				b, ok1 := estate.PyFloatAny(data.V("busbw"))
				t, ok2 := estate.PyFloatAny(data.V("target"))
				if !ok1 || !ok2 {
					okv, msg = false, "bus bandwidth and target in GB/s"
					break
				}
				ks, isS := kind.(string)
				if !isS {
					panic(http.ErrAbortHandler)
				}
				okv, msg = est.SubmitResult(ks, b, t, user, "")
			}
		case "/api/push-credential":
			if data.Truthy("user") && data.Truthy("password") {
				est.SetPushCred(estate.PyStr(data.V("user")), estate.PyStr(data.V("password")))
				est.Audit(user, "push-credential", estate.PyStr(data.V("user")), "held in memory only")
				okv, msg = true, "held in memory until the service restarts"
			} else {
				okv, msg = false, "user and password"
			}
		case "/api/rma/start":
			needDev(n)
			okv, msg = est.RMAStart(n, user, dval(data, "ticket", ""))
		case "/api/rma/identity":
			needDev(n)
			okv, msg = est.RMAIdentity(n, user, dval(data, "value", ""))
		case "/api/rma/upload":
			needDev(n)
			okv, msg = est.RMAUpload(n, dval(data, "text", ""), user)
		case "/api/rma/check":
			needDev(n)
			okv, msg = est.RMACheck(n, estate.PyStr(dval(data, "key", "")), user)
		case "/api/rma/apply":
			needDev(n)
			okv, msg = est.RMAApply(n, dval(data, "source", "planned"), user)
		case "/api/rma/mnha-restore":
			needDev(n)
			okv, msg = est.MNHARestore(n, user)
		case "/api/handover/final-backup":
			if u.Role == "admin" {
				okv, msg = est.HandoverFinalBackup(user)
			} else {
				okv, msg = false, "admins only"
			}
		case "/api/handover/received":
			if u.Role == "admin" && u.Org == "customer" {
				okv, msg = est.HandoverReceived(user, dval(data, "sha", ""))
			} else {
				okv, msg = false, "a customer admin confirms receipt"
			}
		case "/api/release-vendor":
			if u.Role != "admin" || u.Org != "customer" {
				okv, msg = false, "refused: Release DWR access is for the customer's admins"
			} else {
				okv, msg = est.ReleaseVendor(user, estate.PyStr(dval(data, "password", "")), data, true)
			}
		case "/api/tuning/profile":
			ok, m, prof := est.TuningProfile(user, dval(data, "nic", ""), dval(data, "port", ""))
			j(w, okCode(ok), pyjson.O("ok", ok, "msg", m, "profile", prof))
			return
		case "/api/tuning/propose":
			vals := pyjson.NewMap()
			if v := data.V("values"); pyjson.Truthy(v) {
				m, isM := v.(*pyjson.Map)
				if !isM {
					panic(http.ErrAbortHandler) // Python: AttributeError (.items() of a non-object)
				}
				vals = m
			}
			okv, msg = est.TuningPropose(vals, pyHeadS(estate.PyStr(dval(data, "note", "")), 300), user)
		case "/api/tuning/apply", "/api/tuning/approve":
			v, err := pyIntArg(dval(data, "v", int64(0)))
			if err != nil {
				okv, msg = false, "version: a number"
			} else if p == "/api/tuning/apply" {
				okv, msg = est.TuningApply(v, estate.PyStr(dval(data, "scope", "test")), user, false)
			} else {
				okv, msg = est.TuningApprove(v, user)
			}
		case "/api/tuning/rollout":
			okv, msg = est.TuningRollout(user)
		case "/api/runner/retry":
			okv, msg = est.RunnerRetry(dval(data, "key", ""), user)
		case "/api/promote":
			if est.RoleNow() == "standby" {
				okv, msg = est.Promote()
			} else {
				okv, msg = false, "already primary"
			}
		default:
			j(w, 404, pyjson.O("error", "not found"))
			return
		}
		j(w, okCode(okv), pyjson.O("ok", okv, "msg", msg))
	}
	return guard("console", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			get(w, r)
		case "POST":
			post(w, r)
		default:
			unsupported(w, r)
		}
	})
}

// needDev: an action that needs a device and was given none ends the request without a reply (Python: KeyError).
func needDev(n string) {
	if n == "" {
		panic(http.ErrAbortHandler)
	}
}

// pyIntArg is int(x) for a JSON value: a ValueError is an error; a TypeError (null, list, object) ends the request.
func pyIntArg(v any) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) {
			panic(http.ErrAbortHandler)
		}
		return int64(x), nil
	case string:
		return estate.PyInt(x)
	}
	panic(http.ErrAbortHandler)
}

func pyHeadS(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func pyStripS(s string) string { return strings.Trim(s, " \t\n\r\x0b\x0c") }

// ------------------------------------------------------------------------------------------------ serve

// Listen opens one listener, as Python's listen(): the configured address, else prov_ip, else all addresses.
func Listen(est *estate.Estate, key string, port int64) (net.Listener, string, error) {
	addr := est.Conf.Str(key)
	if addr == "" {
		addr = est.Plan.M("site").Str("prov_ip")
	}
	if addr == "" {
		addr = "0.0.0.0"
	}
	l, err := net.Listen("tcp4", net.JoinHostPort(addr, strconv.FormatInt(port, 10)))
	if err == nil {
		return l, addr, nil
	}
	if !strings.Contains(err.Error(), "cannot assign requested address") || addr == "0.0.0.0" {
		return nil, addr, err
	}
	if est.Role == "standby" && est.Conf.Str(key) == "" {
		est.Event(fmt.Sprintf("standby: %s is not on this host (the primary holds it); %s listens on all addresses -- set %s in estate.conf", addr, key[:len(key)-5], key), "warn")
		l, err = net.Listen("tcp4", net.JoinHostPort("0.0.0.0", strconv.FormatInt(port, 10)))
		return l, "0.0.0.0", err
	}
	est.Event(fmt.Sprintf("cannot listen on %s:%d: the address is not on this host. Add it to the services interface (bond0) or set %s in estate.conf", addr, port, key), "fail")
	return nil, addr, err
}

// NewServer is an http.Server with the 30 s per-connection limit of the Python service.
func NewServer(h http.Handler, tlsConf *tls.Config) *http.Server {
	return &http.Server{Handler: h, TLSConfig: tlsConf, ReadHeaderTimeout: 30 * time.Second, ReadTimeout: 30 * time.Second,
		IdleTimeout: 30 * time.Second, ErrorLog: nil}
}

func randHex(n int) string { return estate.RandHex(n) }

var _ = url.Parse
