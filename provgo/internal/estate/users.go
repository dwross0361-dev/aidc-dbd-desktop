package estate

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"aidc-dbd/provgo/internal/pyjson"
)

// Roles and planes of console logins.
var (
	Roles  = []string{"admin", "operator", "viewer"}
	Planes = []string{"oob", "edge", "fe", "be"}
)

// planesArg is _planes: a list or a comma-separated string, normalized to plane order.
func planesArg(v any) (string, error) {
	var raw []string
	switch x := v.(type) {
	case []any:
		for _, p := range x {
			raw = append(raw, PyStr(p))
		}
	case []string:
		raw = x
	default:
		s := ""
		if pyjson.Truthy(v) {
			s = PyStr(v)
		}
		raw = strings.Split(s, ",")
	}
	var pl, bad []string
	for _, x := range raw {
		if t := pyStrip(x); t != "" {
			pl = append(pl, t)
		}
	}
	for _, x := range pl {
		if !containsStr(Planes, x) {
			bad = append(bad, x)
		}
	}
	if len(bad) > 0 {
		return "", fmt.Errorf("unknown plane(s): %s (oob, edge, fe, be)", strings.Join(bad, ", "))
	}
	var out []string
	for _, p := range Planes {
		if containsStr(pl, p) {
			out = append(out, p)
		}
	}
	return strings.Join(out, ","), nil
}

// FloorRows are the plan's Floor view row labels, in floor order.
func (e *Estate) FloorRows() []string {
	var out []string
	for _, r := range e.Plan.L("floor") {
		if m, ok := r.(*pyjson.Map); ok && m.Str("row") != "" {
			out = append(out, m.Str("row"))
		}
	}
	return out
}

var rowRE = regexp.MustCompile(`^P(\d+)\.(\d+)(?:-P?(\d+)\.(\d+))?$`)
var rowSplit = regexp.MustCompile(`[,\s]+`)

// rowsArg is _rows (HS v1.91): Floor view rows ('P1.1-P4.1, P7.1'), validated against the floor, in floor-number order.
func (e *Estate) rowsArg(v any) (string, error) {
	var raw []string
	switch x := v.(type) {
	case []any:
		for _, p := range x {
			raw = append(raw, PyStr(p))
		}
	default:
		if pyjson.Truthy(v) {
			raw = rowSplit.Split(PyStr(v), -1)
		}
	}
	known := e.FloorRows()
	var out []string
	for _, it := range raw {
		it = strings.ToUpper(pyStrip(it))
		if it == "" {
			continue
		}
		m := rowRE.FindStringSubmatch(it)
		if m == nil {
			return "", fmt.Errorf("rows: %s is not a Floor view row (P<pod>.<row>, or a range P1.1-P4.1)", it)
		}
		a0, _ := strconv.Atoi(m[1])
		a1, _ := strconv.Atoi(m[2])
		var got []string
		if m[3] == "" {
			got = []string{fmt.Sprintf("P%d.%d", a0, a1)}
		} else {
			b0, _ := strconv.Atoi(m[3])
			b1, _ := strconv.Atoi(m[4])
			switch {
			case a1 == b1 && b0 >= a0:
				for q := a0; q <= b0; q++ {
					got = append(got, fmt.Sprintf("P%d.%d", q, a1))
				}
			case a0 == b0 && b1 >= a1:
				for q := a1; q <= b1; q++ {
					got = append(got, fmt.Sprintf("P%d.%d", a0, q))
				}
			default:
				return "", fmt.Errorf("rows: %s: a range runs over pods at one row number or over rows of one pod", it)
			}
		}
		var bad []string
		for _, g := range got {
			if len(known) > 0 && !containsStr(known, g) {
				bad = append(bad, g)
			}
		}
		if len(bad) > 0 {
			return "", fmt.Errorf("rows: %s not on this site's floor (%s ... %s)", strings.Join(bad, ", "), known[0], known[len(known)-1])
		}
		for _, g := range got {
			if !containsStr(out, g) {
				out = append(out, g)
			}
		}
	}
	key := func(g string) (int, int) {
		p := strings.SplitN(g[1:], ".", 2)
		x, _ := strconv.Atoi(p[0])
		y, _ := strconv.Atoi(p[1])
		return x, y
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		ax, ay := key(a)
		bx, by := key(b)
		if ax != bx {
			return ax < bx
		}
		return ay < by
	})
	return strings.Join(out, ","), nil
}

type hold struct {
	user  string
	until float64
}

// DeviceHold (HS v1.91): one person on a device at a time; a console action holds it for two minutes.
// Returns the other holder and seconds left, or "".
func (e *Estate) DeviceHold(n, user string) (string, float64) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	if e.holds == nil {
		e.holds = map[string]hold{}
	}
	if h, ok := e.holds[n]; ok && h.user != user && h.until > Now() {
		return h.user, h.until - Now()
	}
	e.holds[n] = hold{user, Now() + 120}
	return "", 0
}

// DeviceRow is the Floor view row a row login may act in ("" = plane-wide).
func (e *Estate) DeviceRow(n string) string {
	if d, ok := e.Devs[n]; ok && d != nil {
		return d.Str("row")
	}
	return ""
}

// UserAdd creates a login (temporary password when pw is ""), returns the password.
func (e *Estate) UserAdd(name string, role any, pw string, planes any, mustChange bool, org string, rows ...any) (string, error) {
	if pw == "" {
		pw = TempPassword(16)
	}
	rs, _ := role.(string)
	rw := ""
	if rs == "operator" && len(rows) > 0 {
		var err error
		if rw, err = e.rowsArg(rows[0]); err != nil {
			return "", err
		}
	}
	e.Mu.Lock()
	defer e.Mu.Unlock()
	if !containsStr(Roles, rs) {
		return "", errors.New("role is admin, operator or viewer")
	}
	pl := ""
	if rs == "operator" {
		var err error
		if pl, err = planesArg(planes); err != nil {
			return "", err
		}
	}
	if e.DB.QueryOne("SELECT 1 FROM users WHERE name=?", name) != nil {
		return "", errors.New("that login exists: reset or update it instead")
	}
	mc := 0
	if mustChange {
		mc = 1
	}
	if _, err := e.DB.Exec("INSERT INTO users(name,pw,role,created,must_change,planes,disabled) VALUES(?,?,?,?,?,?,0)",
		name, PBKDF2(pw, "", 240000), rs, Now(), mc, pl); err != nil {
		return "", errors.New("that login exists: reset or update it instead")
	}
	og := "dwr"
	if org == "customer" {
		og = "customer"
	}
	e.DB.MustExec("UPDATE users SET org=?, rows=? WHERE name=?", og, rw, name)
	return pw, nil
}

// PWRule returns why a new password is refused, or "".
func PWRule(name, nw string, old *string) string {
	if len([]rune(nw)) < 12 {
		return "at least 12 characters"
	}
	if strings.ToLower(nw) == strings.ToLower(name) {
		return "must not be the user name"
	}
	if old != nil && nw == *old {
		return "must differ from the current password"
	}
	return ""
}

func (e *Estate) activeAdmins() []string {
	var out []string
	for _, r := range e.DB.Query("SELECT name FROM users WHERE role='admin' AND COALESCE(disabled,0)=0") {
		out = append(out, r.Str("name"))
	}
	return out
}

// UsersView is GET /api/users.
func (e *Estate) UsersView() *pyjson.Map {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	rows := []any{}
	for _, r := range e.DB.Query("SELECT * FROM users ORDER BY name") {
		org := r.V("org")
		if !pyjson.Truthy(org) {
			org = "dwr"
		}
		planes := []any{}
		if r.Truthy("planes") {
			for _, p := range strings.Split(r.Str("planes"), ",") {
				planes = append(planes, p)
			}
		}
		rws := []any{}
		if r.Truthy("rows") {
			for _, p := range strings.Split(r.Str("rows"), ",") {
				rws = append(rws, p)
			}
		}
		rows = append(rows, pyjson.O("name", r.V("name"), "role", r.V("role"), "org", org, "planes", planes, "rows", rws,
			"must_change", r.Truthy("must_change"), "disabled", r.Truthy("disabled"), "created", r.V("created")))
	}
	return pyjson.O("users", rows, "coverage", e.coverage())
}

func (e *Estate) coverage() *pyjson.Map {
	rows := e.DB.Query("SELECT role, planes FROM users WHERE COALESCE(disabled,0)=0")
	keys := e.buildKeys()
	if len(keys) == 0 {
		keys = Planes
	}
	per := pyjson.NewMap()
	short := []any{}
	for _, k := range keys {
		c := int64(0)
		for _, r := range rows {
			if r.Str("role") == "admin" || (r.Str("role") == "operator" && containsStr(strings.Split(r.Str("planes"), ","), k)) {
				c++
			}
		}
		per.Set(k, c)
		if c < 2 {
			short = append(short, k)
		}
	}
	return pyjson.O("per_plane", per, "short", short, "admins", int64(len(e.activeAdmins())))
}

// UserUpdate changes role, planes, disabled or org; the last two admins are protected.
func (e *Estate) UserUpdate(actor, name string, role, planes, disabled, org any, rowsOpt ...any) (bool, string) {
	e.Mu.Lock()
	r := e.DB.QueryOne("SELECT * FROM users WHERE name=?", name)
	if r == nil {
		e.Mu.Unlock()
		return false, "no such login"
	}
	rv := role
	if !pyjson.Truthy(rv) {
		rv = r.V("role")
	}
	rs, _ := rv.(string)
	if !containsStr(Roles, rs) {
		e.Mu.Unlock()
		return false, "role is admin, operator or viewer"
	}
	dis := r.Int("disabled")
	if disabled != nil {
		dis = 0
		if pyjson.Truthy(disabled) {
			dis = 1
		}
	}
	losing := r.Str("role") == "admin" && !r.Truthy("disabled") && (rs != "admin" || dis != 0)
	if losing && len(e.activeAdmins()) <= 2 {
		e.Mu.Unlock()
		return false, "refused: the last two admins cannot be disabled or demoted (add another admin first)"
	}
	pl := ""
	if rs == "operator" {
		src := planes
		if planes == nil {
			src = r.V("planes")
		}
		var err error
		if pl, err = planesArg(src); err != nil {
			e.Mu.Unlock()
			return false, err.Error()
		}
	}
	rw := ""
	if rs == "operator" { // HS v1.90
		var src any = r.V("rows")
		if len(rowsOpt) > 0 && rowsOpt[0] != nil {
			src = rowsOpt[0]
		}
		var err error
		if rw, err = e.rowsArg(src); err != nil {
			e.Mu.Unlock()
			return false, err.Error()
		}
	}
	og := r.V("org")
	if os, ok := org.(string); ok && (os == "dwr" || os == "customer") {
		og = os
	}
	e.DB.MustExec("UPDATE users SET role=?, planes=?, disabled=?, org=?, rows=? WHERE name=?", rs, pl, dis, og, rw, name)
	if dis != 0 {
		e.DB.MustExec("DELETE FROM sessions WHERE user=?", name)
	}
	plS := pl
	if plS == "" {
		plS = "-"
	}
	det := fmt.Sprintf("role=%s planes=%s disabled=%s org=%s", rs, plS, PyStr(dis != 0), PyStr(og))
	if rw != "" {
		det += " rows=" + rw
	}
	e.audit(actor, "user-update", name, det)
	e.Mu.Unlock()
	return true, "updated"
}

// UserReset issues a temporary password and ends the user's sessions.
func (e *Estate) UserReset(actor, name string) (bool, string) {
	pw := TempPassword(16)
	e.Mu.Lock()
	defer e.Mu.Unlock()
	if e.DB.QueryOne("SELECT 1 FROM users WHERE name=?", name) == nil {
		return false, "no such login"
	}
	e.DB.MustExec("UPDATE users SET pw=?, must_change=1 WHERE name=?", PBKDF2(pw, "", 240000), name)
	e.DB.MustExec("DELETE FROM sessions WHERE user=?", name)
	e.audit(actor, "password-reset", name, "temporary password issued; change required at login")
	return true, pw
}

// ChangePassword replaces the user's password; the other sessions end.
func (e *Estate) ChangePassword(name, old, nw, keepToken string) (bool, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	r := e.DB.QueryOne("SELECT * FROM users WHERE name=?", name)
	if r == nil || !PBKDF2OK(old, r.Str("pw")) {
		return false, "current password is wrong"
	}
	if why := PWRule(name, nw, &old); why != "" {
		return false, "new password: " + why
	}
	e.DB.MustExec("UPDATE users SET pw=?, must_change=0 WHERE name=?", PBKDF2(nw, "", 240000), name)
	e.DB.MustExec("DELETE FROM sessions WHERE user=? AND token<>?", name, keepToken)
	e.audit(name, "password-change", name, "")
	return true, "password changed"
}

// Who is a console session.
type Who struct {
	User, Role, Org, Token string
	Planes                 []string
	Rows                   []string // HS v1.91: Floor view rows ("" = the whole plane)
	MustChange             bool
}

// Map is the whoami JSON (without the token).
func (w *Who) Map() *pyjson.Map {
	pl := make([]any, len(w.Planes))
	for i, p := range w.Planes {
		pl[i] = p
	}
	rw := make([]any, len(w.Rows))
	for i, p := range w.Rows {
		rw[i] = p
	}
	return pyjson.O("user", w.User, "role", w.Role, "planes", pl, "must_change", w.MustChange, "org", w.Org, "rows", rw)
}

// MayAct: admin any plane, operator its own planes, viewer none. HS v1.91: an operator with rows acts only on the
// devices racked in those rows; plane-wide devices and plane actions need an operator with the whole plane.
func (e *Estate) MayAct(w *Who, plane string, device ...string) bool {
	if w.Role == "admin" {
		return true
	}
	if w.Role != "operator" {
		return false
	}
	ok := true
	if plane == "all" {
		for _, k := range e.buildKeys() {
			if !containsStr(w.Planes, k) {
				ok = false
			}
		}
	} else {
		ok = containsStr(w.Planes, plane)
	}
	if ok && len(w.Rows) > 0 {
		row := ""
		if len(device) > 0 && device[0] != "" {
			row = e.DeviceRow(device[0])
		}
		return row != "" && containsStr(w.Rows, row)
	}
	return ok
}

// Login checks a password (outside the lock, against a dummy for unknown names) and opens a 12 h session.
func (e *Estate) Login(name, pw string) string {
	e.Mu.Lock()
	r := e.DB.QueryOne("SELECT pw, disabled FROM users WHERE name=?", name)
	e.Mu.Unlock()
	stored := ""
	if r != nil && !r.Truthy("disabled") {
		stored = r.Str("pw")
	}
	h := stored
	if h == "" {
		h = DummyPW
	}
	if !PBKDF2OK(pw, h) || stored == "" {
		return ""
	}
	e.Mu.Lock()
	defer e.Mu.Unlock()
	r = e.DB.QueryOne("SELECT pw, disabled FROM users WHERE name=?", name)
	if r == nil || r.Truthy("disabled") || r.Str("pw") != stored {
		return ""
	}
	tok := tokenURLSafe(32)
	e.DB.MustExec("INSERT INTO sessions(token,user,expires) VALUES(?,?,?)", tok, name, Now()+12*3600)
	return tok
}

// Session returns the login of a session token, or nil.
func (e *Estate) Session(tok string) *Who {
	if tok == "" {
		return nil
	}
	e.Mu.Lock()
	defer e.Mu.Unlock()
	r := e.DB.QueryOne("SELECT s.user, u.role, u.planes, u.must_change, u.org, u.rows FROM sessions s JOIN users u ON u.name=s.user "+
		"WHERE token=? AND expires>? AND COALESCE(u.disabled,0)=0", tok, Now())
	if r == nil {
		return nil
	}
	w := &Who{User: r.Str("user"), Role: r.Str("role"), Org: r.Str("org"), Token: tok, MustChange: r.Truthy("must_change")}
	if w.Org == "" {
		w.Org = "dwr"
	}
	if p := r.Str("planes"); p != "" {
		w.Planes = strings.Split(p, ",")
	}
	if p := r.Str("rows"); p != "" {
		w.Rows = strings.Split(p, ",")
	}
	return w
}

// Logout ends a session.
func (e *Estate) Logout(tok string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	e.DB.MustExec("DELETE FROM sessions WHERE token=?", tok)
}

// UserExists reports whether a login exists.
func (e *Estate) UserExists(name string) bool {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	return e.DB.QueryOne("SELECT 1 FROM users WHERE name=?", name) != nil
}

// UserCount is the number of logins.
func (e *Estate) UserCount() int {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	return int(e.DB.QueryOne("SELECT count(*) AS c FROM users").Int("c"))
}

func (e *Estate) customerAdmins() int {
	return len(e.DB.Query("SELECT name FROM users WHERE role='admin' AND org='customer' AND COALESCE(disabled,0)=0"))
}
