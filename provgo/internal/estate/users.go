package estate

import (
	"errors"
	"fmt"
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

// UserAdd creates a login (temporary password when pw is ""), returns the password.
func (e *Estate) UserAdd(name string, role any, pw string, planes any, mustChange bool, org string) (string, error) {
	if pw == "" {
		pw = TempPassword(16)
	}
	e.Mu.Lock()
	defer e.Mu.Unlock()
	rs, _ := role.(string)
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
	e.DB.MustExec("UPDATE users SET org=? WHERE name=?", og, name)
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
		rows = append(rows, pyjson.O("name", r.V("name"), "role", r.V("role"), "org", org, "planes", planes,
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
func (e *Estate) UserUpdate(actor, name string, role, planes, disabled, org any) (bool, string) {
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
	og := r.V("org")
	if os, ok := org.(string); ok && (os == "dwr" || os == "customer") {
		og = os
	}
	e.DB.MustExec("UPDATE users SET role=?, planes=?, disabled=?, org=? WHERE name=?", rs, pl, dis, og, name)
	if dis != 0 {
		e.DB.MustExec("DELETE FROM sessions WHERE user=?", name)
	}
	plS := pl
	if plS == "" {
		plS = "-"
	}
	e.audit(actor, "user-update", name, fmt.Sprintf("role=%s planes=%s disabled=%s org=%s", rs, plS, PyStr(dis != 0), PyStr(og)))
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
	MustChange             bool
}

// Map is the whoami JSON (without the token).
func (w *Who) Map() *pyjson.Map {
	pl := make([]any, len(w.Planes))
	for i, p := range w.Planes {
		pl[i] = p
	}
	return pyjson.O("user", w.User, "role", w.Role, "planes", pl, "must_change", w.MustChange, "org", w.Org)
}

// MayAct: admin any plane, operator its own planes, viewer none.
func (e *Estate) MayAct(w *Who, plane string) bool {
	if w.Role == "admin" {
		return true
	}
	if w.Role != "operator" {
		return false
	}
	if plane == "all" {
		for _, k := range e.buildKeys() {
			if !containsStr(w.Planes, k) {
				return false
			}
		}
		return true
	}
	return containsStr(w.Planes, plane)
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
	r := e.DB.QueryOne("SELECT s.user, u.role, u.planes, u.must_change, u.org FROM sessions s JOIN users u ON u.name=s.user "+
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
