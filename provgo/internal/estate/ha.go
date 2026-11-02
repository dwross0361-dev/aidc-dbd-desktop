package estate

// Standby host and cross-site identity (Python: peer_fetch, sync_token, snapshot, sync_worker, peer_pin, pin_refused,
// receive_snapshot, promote, inventory, foreign_worker).

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aidc-dbd/provgo/internal/pyjson"
)

// PinMismatch is a peer whose certificate is not the pinned one (Error() is the fingerprint it showed).
type PinMismatch struct{ FP string }

func (p *PinMismatch) Error() string { return p.FP }

// PeerFetch is one HTTPS request to a peer estate service. Its certificate's SHA-256 is compared with pin before any
// header (or token) is sent; pin "" = first contact: nothing is sent. Returns (fingerprint, body).
func PeerFetch(rawURL, path, pin string, body []byte, headers map[string]string, timeout time.Duration) (string, []byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "", nil, fmt.Errorf("peer URL must be https://host[:port]: %s", rawURL)
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	d := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(d, "tcp", net.JoinHostPort(u.Hostname(), port), &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
	if err != nil {
		return "", nil, errors.New(netErrText(err))
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", nil, errors.New("peer sent no certificate")
	}
	sum := sha256.Sum256(certs[0].Raw)
	fp := hex.EncodeToString(sum[:])
	if pin == "" {
		return fp, nil, nil
	}
	if !hmac.Equal([]byte(fp), []byte(pin)) {
		return fp, nil, &PinMismatch{fp}
	}
	method := "GET"
	var rd io.Reader
	if body != nil {
		method = "POST"
		rd = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(method, "https://"+u.Host+strings.TrimRight(u.Path, "/")+path, rd)
	req.Host = u.Host
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "estate")
	req.Close = true
	if body != nil {
		req.ContentLength = int64(len(body))
		_ = conn.SetDeadline(time.Now().Add(timeout + time.Duration(len(body)/(1<<20))*time.Second))
	}
	if err := req.Write(conn); err != nil {
		return fp, nil, errors.New(netErrText(err))
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return fp, nil, errors.New(netErrText(err))
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return fp, nil, errors.New(netErrText(err))
	}
	if resp.StatusCode >= 400 {
		return fp, nil, fmt.Errorf("HTTP %d from %s%s", resp.StatusCode, rawURL, path)
	}
	return fp, data, nil
}

// netErrText words a network error as Python's OSError does where it can.
func netErrText(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timed out"
	}
	s := pyErrno(err)
	if strings.HasPrefix(s, "[Errno") {
		return s
	}
	return err.Error()
}

func (e *Estate) syncToken() string { return e.Secrets()["ESTATE_SYNC_TOKEN"] }

// SyncToken is the standby sync token (secrets.env ESTATE_SYNC_TOKEN).
func (e *Estate) SyncToken() string { return e.syncToken() }

// snapshot copies the database to snapshot.db (Python: sqlite3 backup; here VACUUM INTO: the same rows).
func (e *Estate) snapshot() (string, error) {
	p := filepath.Join(e.Conf.Str("state_dir"), "snapshot.db")
	e.Mu.Lock()
	defer e.Mu.Unlock()
	_ = os.Remove(p)
	if _, err := e.DB.Exec("VACUUM INTO ?", p); err != nil {
		return "", err
	}
	return p, nil
}

// SyncWorker sends the state to the standby host every sync_every_s (a primary with peer_url and a sync token).
func (e *Estate) SyncWorker() {
	for {
		every := 60.0
		if v, ok := e.Conf.Get("sync_every_s"); ok {
			every = pyFloatAnyZ(v)
		}
		time.Sleep(time.Duration(every * float64(time.Second)))
		if e.roleNow() != "primary" || !e.Conf.Truthy("peer_url") || e.syncToken() == "" {
			continue
		}
		func() {
			defer func() {
				if x := recover(); x != nil {
					fmt.Fprintf(os.Stderr, "sync: %v\n", x)
				}
			}()
			u := e.Conf.Str("peer_url")
			pin, err := e.peerPin(u, e.Conf.V("peer_cert_sha256"), "standby")
			if err != nil {
				e.syncErr(u, err, "standby", "peer_cert_sha256")
				return
			}
			if pin == "" {
				return
			}
			p, err := e.snapshot()
			if err != nil {
				fmt.Fprintf(os.Stderr, "sync: %s\n", err)
				return
			}
			body, err := os.ReadFile(p)
			if err != nil {
				fmt.Fprintf(os.Stderr, "sync: %s\n", pyErrno(err))
				return
			}
			_, _, err = PeerFetch(u, "/sync/upload", pin, body, map[string]string{"X-Estate-Sync": e.syncToken(), "Content-Type": "application/octet-stream"}, 30*time.Second)
			if err != nil {
				e.syncErr(u, err, "standby", "peer_cert_sha256")
				return
			}
			e.Mu.Lock()
			e.LastSync = Now()
			e.KVSet("last_sync", e.LastSync)
			e.Mu.Unlock()
		}()
	}
}

func (e *Estate) syncErr(u string, err error, what, confKey string) {
	var pm *PinMismatch
	if errors.As(err, &pm) {
		e.pinRefusedEv(u, pm.FP, what, confKey)
		return
	}
	fmt.Fprintf(os.Stderr, "sync: %s\n", err)
}

// peerPin is the certificate fingerprint a peer must present: a pin in estate.conf wins; otherwise the one recorded at
// first contact (trust on first use, logged). "" = not reachable yet (an error).
func (e *Estate) peerPin(rawURL string, confPin any, what string) (string, error) {
	key := rawURL
	if u, err := url.Parse(rawURL); err == nil {
		key = u.Host
	}
	if pyjson.Truthy(confPin) {
		fp := NormFP(PyStr(confPin))
		if fp == "" {
			return "", fmt.Errorf("%s certificate pin is not a SHA-256 fingerprint: %s", what, PyStr(confPin))
		}
		return fp, nil
	}
	e.Mu.Lock()
	pins := e.kvGetMap("peer_pins")
	e.Mu.Unlock()
	if pins.Truthy(key) {
		return PyStr(pins.V(key)), nil
	}
	fp, _, err := PeerFetch(rawURL, "", "", nil, nil, 20*time.Second)
	if err != nil {
		return "", err
	}
	e.Mu.Lock()
	pins = e.kvGetMap("peer_pins")
	pins.Set(key, fp)
	e.KVSet("peer_pins", pins)
	e.ev(fmt.Sprintf("Pinned the %s certificate at %s on first contact: SHA-256 %s. Compare it with 'estate_build.py "+
		"fingerprint' on that host; if it differs, remove it and set the right value in estate.conf", what, key, fp), "warn", "")
	e.Mu.Unlock()
	return fp, nil
}

// pinRefusedEv logs one fail event per unexpected fingerprint (the workers retry every cycle).
func (e *Estate) pinRefusedEv(rawURL, got, what, confKey string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	if e.pinRefused == nil {
		e.pinRefused = map[[2]string]bool{}
	}
	k := [2]string{rawURL, got}
	if e.pinRefused[k] {
		return
	}
	e.pinRefused[k] = true
	e.ev(fmt.Sprintf("Refused the %s at %s: its certificate (SHA-256 %s) is not the pinned one, so no token was sent. If its "+
		"certificate was replaced, set %s in estate.conf to the new 'estate_build.py fingerprint' value and restart", what, rawURL, got, confKey), "fail", "")
}

// ReceiveSnapshot (standby): keep the primary's state, and mirror its open waves into this host's Kea.
func (e *Estate) ReceiveSnapshot(body []byte) error {
	p := filepath.Join(e.Conf.Str("state_dir"), "primary.db")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	if err := checkSnapshot(tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	src, err := openPlain(p)
	if err != nil {
		return err
	}
	defer src.Close()
	devs := src.Query("SELECT name, data FROM devices")
	binds := src.Query("SELECT * FROM bindings")
	blocked := src.Query("SELECT * FROM blocked")
	kvs := src.Query("SELECT * FROM kv")
	e.Mu.Lock()
	defer e.Mu.Unlock()
	for _, r := range devs {
		if s := e.St[r.Str("name")]; s != nil {
			m, err := pyjson.LoadsMap([]byte(r.Str("data")))
			if err != nil {
				return err
			}
			s.Update(m)
		}
	}
	for _, r := range binds {
		e.Ident.Set(r.Str("name"), Identity{PyStr(r.V("kind")), PyStr(r.V("value")), PyStr(r.V("source"))})
	}
	for _, r := range blocked {
		e.Blocked[PyStr(r.V("value"))] = PyStr(r.V("reason"))
	}
	kv := pyjson.NewMap()
	for _, r := range kvs {
		v, err := pyjson.Loads([]byte(r.Str("v")))
		if err != nil {
			return err
		}
		kv.Set(r.Str("k"), v)
	}
	if b := kv.M("bstate"); b != nil {
		e.BState.Update(b)
	}
	if r := kv.M("run"); kv.Has("run") && r != nil {
		e.Run = r
	}
	e.LastSync = Now()
	e.KeaDirty = true
	return nil
}

// openPlain opens a SQLite file without the schema script (a snapshot from the primary).
func openPlain(path string) (*DB, error) {
	return OpenDBRaw(path)
}

func checkSnapshot(p string) (err error) {
	d, err := OpenDBRaw(p)
	if err != nil {
		return err
	}
	defer d.Close()
	defer func() {
		if x := recover(); x != nil {
			err = fmt.Errorf("%v", x)
		}
	}()
	d.Query("SELECT count(*) FROM devices")
	return nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// Promote: this standby host takes over as primary from the last synced state.
func (e *Estate) Promote() (bool, string) {
	p := filepath.Join(e.Conf.Str("state_dir"), "primary.db")
	if !isFile(p) {
		return false, "no snapshot from the primary yet"
	}
	e.Mu.Lock()
	defer e.Mu.Unlock()
	since := pyIntAny(e.KVGet("promoted_from", int64(0)))
	local := e.DB.Query("SELECT * FROM events WHERE id > ?", since)
	e.DB.Close()
	live := filepath.Join(e.Conf.Str("state_dir"), "estate.db")
	if err := copyFile(live, fmt.Sprintf("%s.standby-%d", live, int64(Now()))); err != nil {
		panic(err)
	}
	if err := copyFile(p, live); err != nil {
		panic(err)
	}
	for _, suf := range []string{"-journal", "-wal", "-shm"} {
		_ = os.Remove(live + suf)
	}
	db, err := OpenDBRaw(live)
	if err != nil {
		panic(err)
	}
	e.DB = db
	for _, ev := range local {
		txt := ""
		if t, ok := ev.V("text").(string); ok {
			txt = t
		}
		e.DB.MustExec("INSERT INTO events(ts,level,device,stage,flag,text,user) VALUES(?,?,?,?,?,?,?)",
			ev.V("ts"), ev.V("level"), ev.V("device"), ev.V("stage"), ev.V("flag"), "[standby] "+txt, ev.V("user"))
	}
	mx := e.DB.QueryOne("SELECT COALESCE(MAX(id),0) AS m FROM events")
	e.KVSet("promoted_from", mx.V("m"))
	e.loadState()
	e.p2Init()
	e.Role = "primary"
	cj := pyjson.NewMap()
	if b, err := os.ReadFile(e.ConfPath); err == nil {
		if m, err := pyjson.LoadsMap(b); err == nil {
			cj = m
		}
	}
	cj.Set("role", "primary")
	cj.Delete("peer_url")
	prov := e.provIP()
	for _, k := range []string{"http_bind", "console_bind", "tftp_bind"} {
		if v := cj.V(k); pyjson.Truthy(v) && v != "0.0.0.0" && v != prov {
			cj.Delete(k)
		}
	}
	_ = os.WriteFile(e.ConfPath, []byte(pyjson.DumpsIndent(cj, 1)), 0o644)
	e.ev(fmt.Sprintf("This host promoted to primary (standby events merged: %d)", len(local)), "warn", "")
	e.ev(fmt.Sprintf("Devices fetch from %s: add it to this host (ip addr add %s/<len> dev bond0), then restart "+
		"estate-build so it serves there", prov, prov), "warn", "")
	return true, "promoted"
}

// Inventory is GET /inventory: every identity this site holds (bound and seen on the wire).
func (e *Estate) Inventory() *pyjson.Map {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	items := []any{}
	for _, n := range e.Ident.Keys() {
		i, _ := e.Ident.Get(n)
		items = append(items, pyjson.O("device", n, "kind", i.Kind, "value", i.Value))
	}
	for _, n := range e.DevOrder {
		s := e.St[n]
		for _, k := range []string{"serial", "mac"} {
			if s.Truthy(k) {
				kind := "mac"
				if k == "serial" {
					kind = "sn"
				}
				items = append(items, pyjson.O("device", n, "kind", kind, "value", s.V(k)))
			}
		}
	}
	return pyjson.O("site", e.site().V("code"), "items", items)
}

// ForeignWorker reads the inventories of the peer sites (conf "sites") every sites_every_s.
func (e *Estate) ForeignWorker() {
	for {
		e.foreignPass()
		every := 3600.0
		if v, ok := e.Conf.Get("sites_every_s"); ok {
			every = pyFloatAnyZ(v)
		}
		time.Sleep(time.Duration(every * float64(time.Second)))
	}
}

func (e *Estate) foreignPass() {
	defer func() {
		if x := recover(); x != nil {
			fmt.Fprintf(os.Stderr, "sites: %v\n", x)
		}
	}()
	tok := e.Secrets()["INVENTORY_TOKEN"]
	got := pyjson.NewMap()
	for _, pv := range e.Conf.L("sites") {
		p, ok := pv.(*pyjson.Map)
		if !ok {
			continue
		}
		code := PyStr(dget(p, "code", "?"))
		key := p.Str("url")
		if p.Truthy("code") {
			key = PyStr(p.V("code"))
		}
		err := func() error {
			pin, err := e.peerPin(p.Str("url"), p.V("cert_sha256"), "site "+code)
			if err != nil {
				return err
			}
			t := tok
			if p.Truthy("token") {
				t = PyStr(p.V("token"))
			}
			_, raw, err := PeerFetch(p.Str("url"), "/inventory", pin, nil, map[string]string{"X-Estate-Inventory": t}, 20*time.Second)
			if err != nil {
				return err
			}
			j, err := pyjson.LoadsMap(raw)
			if err != nil {
				return err
			}
			site := code
			if j.Truthy("site") {
				site = PyStr(j.V("site"))
			}
			items := j.L("items")
			for _, iv := range items {
				it, _ := iv.(*pyjson.Map)
				v := strings.ToUpper(PyStr(it.V("value")))
				if pyEq(it.V("kind"), "mac") {
					v = NormMACAny(it.V("value"))
				}
				got.Set(v, []any{site, it.V("device")})
			}
			e.Mu.Lock()
			e.ForeignSync.Set(key, pyjson.O("ok", true, "ts", Now(), "n", int64(len(items))))
			e.Mu.Unlock()
			return nil
		}()
		if err != nil {
			var pm *PinMismatch
			e.Mu.Lock()
			if errors.As(err, &pm) {
				e.Mu.Unlock()
				e.pinRefusedEv(p.Str("url"), pm.FP, "site "+code, "sites[].cert_sha256")
				e.Mu.Lock()
				e.ForeignSync.Set(key, pyjson.O("ok", false, "ts", Now(), "error", "certificate is not the pinned one"))
			} else {
				e.ForeignSync.Set(key, pyjson.O("ok", false, "ts", Now(), "error", pyHead(err.Error(), 120)))
			}
			e.Mu.Unlock()
		}
	}
	e.Mu.Lock()
	defer e.Mu.Unlock()
	fp := pyjson.NewMap()
	for _, k := range got.Keys() {
		l := got.V(k).([]any)
		e.Foreign[k] = [2]string{PyStr(l[0]), PyStr(l[1])}
		fp.Set(k, l)
	}
	e.KVSet("foreign_peers", fp)
	e.KVSet("foreign_sync", e.ForeignSync)
}
