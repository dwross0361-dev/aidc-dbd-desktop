// Package estate is the provisioning service core: plan, state, identity matching, stages, engine and views. Every
// exported method that touches state takes the estate lock; lower-case helpers expect it to be held.
package estate

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"aidc-dbd/provgo/internal/pyjson"
)

// Identity is a device's serial or MAC.
type Identity struct {
	Kind, Value, Source string
}

// IdentMap keeps device -> identity in insertion order with a lookup index rebuilt after changes.
type IdentMap struct {
	keys  []string
	m     map[string]Identity
	mac   map[string][2]any
	sn    map[string][2]any
	dirty bool
}

func newIdentMap() *IdentMap { return &IdentMap{m: map[string]Identity{}, dirty: true} }

// Get returns the identity of n.
func (im *IdentMap) Get(n string) (Identity, bool) { i, ok := im.m[n]; return i, ok }

// Set sets n's identity (an existing device keeps its position).
func (im *IdentMap) Set(n string, i Identity) {
	if _, ok := im.m[n]; !ok {
		im.keys = append(im.keys, n)
	}
	im.m[n] = i
	im.dirty = true
}

// Pop removes n.
func (im *IdentMap) Pop(n string) {
	if _, ok := im.m[n]; !ok {
		return
	}
	delete(im.m, n)
	for k, x := range im.keys {
		if x == n {
			im.keys = append(im.keys[:k:k], im.keys[k+1:]...)
			break
		}
	}
	im.dirty = true
}

// Keys returns the devices in order.
func (im *IdentMap) Keys() []string { return im.keys }

func (im *IdentMap) index() {
	if !im.dirty {
		return
	}
	im.mac, im.sn = map[string][2]any{}, map[string][2]any{}
	for pos, n := range im.keys {
		i := im.m[n]
		if i.Kind == "mac" {
			if v := NormMAC(i.Value); v != "" {
				if _, ok := im.mac[v]; !ok {
					im.mac[v] = [2]any{pos, n}
				}
			}
		} else if i.Kind == "sn" && i.Value != "" {
			v := strings.ToUpper(i.Value)
			if _, ok := im.sn[v]; !ok {
				im.sn[v] = [2]any{pos, n}
			}
		}
	}
	im.dirty = false
}

func (i Identity) toMap() *pyjson.Map {
	return pyjson.O("kind", i.Kind, "value", i.Value, "source", i.Source)
}

type wavePos struct {
	build string
	wave  int
}

// Estate is the running service state.
type Estate struct {
	Mu       sync.Mutex
	PlanPath string
	ConfPath string
	Plan     *pyjson.Map
	Digest   string
	Conf     *pyjson.Map
	KeaTmpl  *pyjson.Map
	DB       *DB

	Builds     []*pyjson.Map
	BuildByKey map[string]*pyjson.Map
	DevOrder   []string
	Devs       map[string]*pyjson.Map
	WaveOf     map[string]wavePos
	holds      map[string]hold // HS v1.91: device -> who holds it, until when
	ByIP       map[string]string
	FileOwner  map[string]string
	Foreign    map[string][2]string
	foreignN   int

	St          map[string]*pyjson.Map
	Ident       *IdentMap
	Blocked     map[string]string
	Unclaimed   *pyjson.Map // key -> entry, in order
	Run         *pyjson.Map
	BState      *pyjson.Map
	GateRes     map[[2]string]*pyjson.Map
	AutoRes     *pyjson.Map // "build|check" -> result, in Python's dict order
	Results     *pyjson.Map
	RunnerState *pyjson.Map
	ForeignSync *pyjson.Map
	LastSync    any
	Role        string
	ReportTpl   string // HS v2.02: the report page (console.html carries it)

	KeaDirty  bool
	KeaStatus string
	keaWarned string

	p2once   sync.Once
	p2c      *p2
	gateOnce sync.Once
	sshg     *sshGate // SSH sessions per device (sshgate.go)
	bondHome string

	// Out receives the event log lines (stdout in the service).
	Out func(string)

	push       pushState
	alerts     alertQueue
	pinRefused map[[2]string]bool
}

// New loads the plan, the configuration and the Kea template, and opens the state (as Estate.__init__).
func New(planPath, confPath, keaTemplatePath string) (*Estate, error) {
	raw, err := os.ReadFile(planPath)
	if err != nil {
		return nil, err
	}
	plan, err := pyjson.LoadsMap(raw)
	if err != nil {
		return nil, fmt.Errorf("plan %s: %v", planPath, err)
	}
	sum := sha256.Sum256(raw)
	if Version == "__BUILDER_VER__" { // not stamped at build time: the builder release that wrote the plan (Python carries the same, DECISIONS P4-2)
		if b, ok := plan.V("builder").(string); ok && b != "" {
			Version = b
		}
	}
	e := &Estate{PlanPath: planPath, ConfPath: confPath, Plan: plan, Digest: hex.EncodeToString(sum[:]),
		KeaDirty: true, KeaStatus: "not yet written", Out: func(s string) { os.Stdout.WriteString(s) }}
	e.Conf = LoadConf(confPath)
	if svc := plan.M("service"); svc != nil {
		for _, k := range svc.Keys() {
			e.Conf.SetDefault(k, svc.V(k))
		}
	}
	kb, err := os.ReadFile(keaTemplatePath)
	if err != nil {
		return nil, err
	}
	if e.KeaTmpl, err = pyjson.LoadsMap(kb); err != nil {
		return nil, fmt.Errorf("kea template %s: %v", keaTemplatePath, err)
	}
	if err := os.MkdirAll(e.Conf.Str("state_dir"), 0o755); err != nil {
		return nil, err
	}
	if e.DB, err = OpenDB(filepath.Join(e.Conf.Str("state_dir"), "estate.db")); err != nil {
		return nil, err
	}
	e.index()
	e.loadState()
	e.p2Init()
	return e, nil
}

func (e *Estate) site() *pyjson.Map { return e.Plan.M("site") }
func (e *Estate) siteCode() string  { return e.site().Str("code") }
func (e *Estate) provIP() string    { return e.site().Str("prov_ip") }

func (e *Estate) short(n string) string {
	d := e.Devs[n]
	if d != nil && d.Has("short") {
		return PyStr(d.V("short"))
	}
	return n
}

// ------------------------------------------------------------------------------------- storage

// KVGet reads a kv value.
func (e *Estate) KVGet(k string, def any) any {
	r := e.DB.QueryOne("SELECT v FROM kv WHERE k=?", k)
	if r == nil {
		return def
	}
	v, err := pyjson.Loads([]byte(r.Str("v")))
	if err != nil {
		panic(err)
	}
	return v
}

func (e *Estate) kvGetMap(k string) *pyjson.Map {
	m, _ := e.KVGet(k, nil).(*pyjson.Map)
	if m == nil {
		return pyjson.NewMap()
	}
	return m
}

// KVSet writes a kv value.
func (e *Estate) KVSet(k string, v any) {
	e.DB.MustExec("INSERT OR REPLACE INTO kv(k,v) VALUES(?,?)", k, pyjson.Dumps(v))
}

func (e *Estate) index() {
	P := e.Plan
	e.Builds = nil
	e.BuildByKey = map[string]*pyjson.Map{}
	for _, b := range P.L("builds") {
		bm := b.(*pyjson.Map)
		e.Builds = append(e.Builds, bm)
		e.BuildByKey[bm.Str("key")] = bm
	}
	e.Devs = map[string]*pyjson.Map{}
	e.DevOrder = nil
	for _, d := range P.L("devices") {
		dm := d.(*pyjson.Map)
		n := dm.Str("name")
		if _, ok := e.Devs[n]; !ok {
			e.DevOrder = append(e.DevOrder, n)
		}
		e.Devs[n] = dm
	}
	e.WaveOf = map[string]wavePos{}
	for _, b := range e.Builds {
		for wi, w := range b.L("waves") {
			for _, n := range pyjson.StrList(w.(*pyjson.Map).V("devices")) {
				e.WaveOf[n] = wavePos{b.Str("key"), wi}
			}
		}
	}
	e.ByIP = map[string]string{}
	devs := P.L("devices")
	for _, d := range devs {
		dm := d.(*pyjson.Map)
		if dm.Truthy("ip") {
			e.ByIP[dm.Str("ip")] = dm.Str("name")
		}
	}
	for _, d := range devs {
		dm := d.(*pyjson.Map)
		if dm.Truthy("seed_ip") {
			e.ByIP[dm.Str("seed_ip")] = dm.Str("name")
		}
	}
	for _, d := range devs {
		dm := d.(*pyjson.Map)
		for _, L := range dm.L("ztp_links") {
			e.ByIP[L.(*pyjson.Map).Str("ip")] = dm.Str("name")
		}
	}
	e.FileOwner = map[string]string{}
	for _, d := range devs {
		dm := d.(*pyjson.Map)
		for _, f := range pyjson.StrList(dm.V("files")) {
			e.FileOwner[strings.TrimLeft(f, "/")] = dm.Str("name")
		}
	}
	e.Foreign = map[string][2]string{}
	fdir := filepath.Join(filepath.Dir(e.Conf.Str("secrets")), "foreign-inventory")
	if ents, err := os.ReadDir(fdir); err == nil {
		// os.listdir order is the directory's order; Python's dict keeps the last value either way
		for _, fe := range ents {
			fn := fe.Name()
			if !strings.HasSuffix(fn, ".csv") {
				continue
			}
			site := fn[:len(fn)-4]
			f, err := os.Open(filepath.Join(fdir, fn))
			if err != nil {
				continue
			}
			r := csv.NewReader(f)
			r.FieldsPerRecord = -1
			r.LazyQuotes = true
			for {
				row, err := r.Read()
				if err != nil {
					break
				}
				if len(row) >= 2 {
					v := NormMAC(row[1])
					if v == "" {
						v = strings.ToUpper(pyStrip(row[1]))
					}
					e.Foreign[v] = [2]string{site, pyStrip(row[0])}
				}
			}
			f.Close()
		}
	}
}

func baseState() *pyjson.Map {
	return pyjson.O("stage", int64(0), "flag", "", "detail", "", "ts", pyjson.NewMap(), "lease_ip", "", "mac", "",
		"serial", "", "client_id", "", "circuit", "", "last_seen", int64(0), "attempts", int64(0),
		"fetched", pyjson.NewMap(), "count", int64(0), "rma", nil, "manual", false, "clock", int64(0))
}

func (e *Estate) loadState() {
	rows := map[string]*pyjson.Map{}
	for _, r := range e.DB.Query("SELECT name, data FROM devices") {
		m, err := pyjson.LoadsMap([]byte(r.Str("data")))
		if err != nil {
			panic(err)
		}
		rows[r.Str("name")] = m
	}
	e.St = map[string]*pyjson.Map{}
	for _, n := range e.DevOrder {
		b := baseState()
		b.Update(rows[n])
		e.St[n] = b
	}
	e.Ident = newIdentMap()
	for _, n := range e.DevOrder {
		id := e.Devs[n].M("id")
		if id != nil && id.Truthy("value") {
			e.Ident.Set(n, Identity{PyStr(id.V("kind")), PyStr(id.V("value")), "inventory"})
		}
	}
	for _, r := range e.DB.Query("SELECT * FROM bindings") {
		e.Ident.Set(r.Str("name"), Identity{r.Str("kind"), r.Str("value"), r.Str("source")})
	}
	e.Blocked = map[string]string{}
	for _, r := range e.DB.Query("SELECT * FROM blocked") {
		e.Blocked[r.Str("value")] = r.Str("reason")
	}
	e.Unclaimed = pyjson.NewMap()
	for _, r := range e.DB.Query("SELECT * FROM unclaimed") {
		m, err := pyjson.LoadsMap([]byte(r.Str("data")))
		if err != nil {
			panic(err)
		}
		e.Unclaimed.Set(r.Str("key"), m)
	}
	if m, ok := e.KVGet("run", nil).(*pyjson.Map); ok {
		e.Run = m
	} else {
		e.Run = pyjson.O("state", "idle", "builds", []any{}, "started", int64(0), "plane", "")
	}
	if m, ok := e.KVGet("bstate", nil).(*pyjson.Map); ok {
		e.BState = m
	} else {
		e.BState = pyjson.NewMap()
	}
	for _, b := range e.Builds {
		e.BState.SetDefault(b.Str("key"), pyjson.O("state", "queued", "open_waves", int64(0), "wave_ts", pyjson.NewMap(),
			"gate_ts", int64(0), "passed", int64(0), "override", nil))
	}
	e.GateRes = map[[2]string]*pyjson.Map{}
	for _, r := range e.DB.Query("SELECT * FROM gates") {
		e.GateRes[[2]string{r.Str("build"), r.Str("chk")}] = r
	}
}

// p2Init loads the Phase II state kept in kv (read for views; the workers are later phases).
func (e *Estate) p2Init() {
	e.AutoRes = pyjson.NewMap()
	ar := e.kvGetMap("auto_res")
	for _, k := range ar.Keys() {
		if strings.Contains(k, "|") {
			e.AutoRes.Set(k, ar.V(k))
		}
	}
	e.Results = e.kvGetMap("results")
	e.RunnerState = e.kvGetMap("runners")
	var stale []string
	for _, k := range e.RunnerState.Keys() {
		v, _ := e.RunnerState.V(k).(*pyjson.Map)
		if v != nil && v.Str("state") == "running" {
			stale = append(stale, k)
		}
	}
	for _, k := range stale {
		v := e.RunnerState.M(k)
		v.Set("state", "failed")
		v.Set("retry", false)
		v.Set("ts", Now())
		v.Set("note", "interrupted by a service restart or promote: check its targets, then Retry")
	}
	if len(stale) > 0 {
		e.KVSet("runners", e.RunnerState)
		e.event(fmt.Sprintf("Runner(s) %s were running when the service stopped: marked failed; check, then Retry", strings.Join(stale, ", ")), "warn", "", nil, nil, "")
	}
	e.ForeignSync = e.kvGetMap("foreign_sync")
	e.LastSync = e.KVGet("last_sync", int64(0))
	fp := e.kvGetMap("foreign_peers")
	for _, k := range fp.Keys() {
		l := pyjson.StrList(fp.V(k))
		if len(l) >= 2 {
			e.Foreign[k] = [2]string{l[0], l[1]}
		}
	}
	e.Role = "primary"
	if v, ok := e.Conf.Get("role"); ok {
		e.Role = PyStr(v)
	}
	_ = os.MkdirAll(filepath.Join(e.Conf.Str("state_dir"), "backups"), 0o755)
}

func (e *Estate) saveDev(n string) {
	e.DB.MustExec("INSERT OR REPLACE INTO devices(name,data) VALUES(?,?)", n, pyjson.Dumps(e.St[n]))
}

func (e *Estate) saveRun() {
	e.KVSet("run", e.Run)
	e.KVSet("bstate", e.BState)
}

// event writes one event row. stage/flag nil = the device's current values (NULL without a device).
func (e *Estate) event(text, level, device string, stage, flag any, user string) {
	if level == "" {
		level = "info"
	}
	ts := Now()
	if s, ok := e.St[device]; device != "" && ok {
		if stage == nil {
			stage = s.V("stage")
		}
		if flag == nil {
			flag = s.V("flag")
		}
	}
	e.DB.MustExec("INSERT INTO events(ts,level,device,stage,flag,text,user) VALUES(?,?,?,?,?,?,?)",
		ts, level, device, stage, flag, text, user)
	if level == "warn" || level == "fail" || strings.HasPrefix(text, "Gate passed") || strings.HasPrefix(text, "Build complete") {
		e.alerts.put(pyjson.O("ts", ISO(ts), "level", level, "device", device, "text", text, "site", e.site().V("code")))
	}
	dv := device
	if dv == "" {
		dv = "-"
	}
	if e.Out != nil {
		e.Out(fmt.Sprintf("%s %-4s %s %s\n", ISO(ts), level, dv, text))
	}
}

// ev is event() with defaults.
func (e *Estate) ev(text, level, device string) { e.event(text, level, device, nil, nil, "") }

// Event takes the lock and logs an event.
func (e *Estate) Event(text, level string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	e.ev(text, level, "")
}

func (e *Estate) audit(user, action, target, detail string) {
	if d, ok := e.Devs[target]; ok && d != nil && d.Str("row") != "" { // HS v1.91: device actions name the device's Floor view row
		if detail != "" {
			detail += " "
		}
		detail += "row=" + d.Str("row")
	}
	e.DB.MustExec("INSERT INTO audit(ts,user,action,target,detail) VALUES(?,?,?,?,?)", Now(), user, action, target, detail)
}

// Audit takes the lock and writes an audit row.
func (e *Estate) Audit(user, action, target, detail string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	e.audit(user, action, target, detail)
}

// Secrets reads secrets.env.
func (e *Estate) Secrets() map[string]string { return LoadEnvFile(e.Conf.Str("secrets")) }

func (e *Estate) bstate(k string) *pyjson.Map { return e.BState.M(k) }

func (e *Estate) buildKeys() []string {
	out := make([]string, len(e.Builds))
	for i, b := range e.Builds {
		out[i] = b.Str("key")
	}
	return out
}
