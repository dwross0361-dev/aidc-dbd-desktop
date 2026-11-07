package estate

import (
	"database/sql"
	"fmt"
	"net/url"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"

	"aidc-dbd/provgo/internal/pyjson"
)

// schemaSQL is the Python service's script, whitespace included (SQLite keeps the text in sqlite_master).
const schemaSQL = `
        CREATE TABLE IF NOT EXISTS kv(k TEXT PRIMARY KEY, v TEXT);
        CREATE TABLE IF NOT EXISTS devices(name TEXT PRIMARY KEY, data TEXT);
        CREATE TABLE IF NOT EXISTS bindings(name TEXT PRIMARY KEY, kind TEXT, value TEXT, source TEXT, ts REAL, user TEXT);
        CREATE TABLE IF NOT EXISTS blocked(value TEXT PRIMARY KEY, reason TEXT, ts REAL);
        CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY AUTOINCREMENT, ts REAL, level TEXT, device TEXT,
                                          stage INTEGER, flag TEXT, text TEXT, user TEXT);
        CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY AUTOINCREMENT, ts REAL, user TEXT, action TEXT,
                                         target TEXT, detail TEXT);
        CREATE TABLE IF NOT EXISTS users(name TEXT PRIMARY KEY, pw TEXT, role TEXT, created REAL);
        CREATE TABLE IF NOT EXISTS sessions(token TEXT PRIMARY KEY, user TEXT, expires REAL);
        CREATE TABLE IF NOT EXISTS unclaimed(key TEXT PRIMARY KEY, data TEXT);
        CREATE TABLE IF NOT EXISTS gates(build TEXT, chk TEXT, result TEXT, user TEXT, note TEXT, ts REAL,
                                         PRIMARY KEY(build, chk));
        CREATE TABLE IF NOT EXISTS rma(id INTEGER PRIMARY KEY AUTOINCREMENT, device TEXT, ticket TEXT, old_kind TEXT,
                                       old_value TEXT, new_kind TEXT, new_value TEXT, state TEXT, opened REAL,
                                       closed REAL, user TEXT, checklist TEXT);
        `

// DB is the state database (one connection, used under the estate lock, every statement committed).
type DB struct{ db *sql.DB }

// OpenDBRaw opens a SQLite file as it is (no schema script): a snapshot from the primary, or the promoted state.
func OpenDBRaw(path string) (*DB, error) {
	dsn := "file:" + (&url.URL{Path: path}).EscapedPath() + "?_pragma=busy_timeout(5000)"
	d, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1)
	d.SetMaxIdleConns(1)
	d.SetConnMaxLifetime(0)
	return &DB{db: d}, nil
}

// OpenDB opens (and creates) estate.db with the Python service's schema.
func OpenDB(path string) (*DB, error) {
	x, err := OpenDBRaw(path)
	if err != nil {
		return nil, err
	}
	d := x.db
	if _, err := d.Exec(schemaSQL); err != nil {
		return nil, err
	}
	cols := map[string]bool{}
	for _, r := range x.Query("PRAGMA table_info(users)") {
		cols[pyjson.Str(r.V("name"))] = true
	}
	for _, c := range [][2]string{{"must_change", "INTEGER DEFAULT 0"}, {"planes", "TEXT DEFAULT ''"},
		{"disabled", "INTEGER DEFAULT 0"}, {"org", "TEXT DEFAULT 'dwr'"}, {"rows", "TEXT DEFAULT ''"}} {
		if !cols[c[0]] {
			if _, err := d.Exec(fmt.Sprintf("ALTER TABLE users ADD COLUMN %s %s", c[0], c[1])); err != nil {
				return nil, err
			}
		}
	}
	return x, nil
}

// Close closes the database.
func (x *DB) Close() error { return x.db.Close() }

// Exec runs one statement (autocommit).
func (x *DB) Exec(q string, args ...any) (sql.Result, error) { return x.db.Exec(q, args...) }

// MustExec runs one statement; a failure is fatal for the request, as in Python.
func (x *DB) MustExec(q string, args ...any) {
	if _, err := x.db.Exec(q, args...); err != nil {
		panic(fmt.Errorf("sqlite: %v (%s)", err, q))
	}
}

// Query returns rows as ordered maps (column order), values int64/float64/string/nil.
func (x *DB) Query(q string, args ...any) []*pyjson.Map {
	rows, err := x.db.Query(q, args...)
	if err != nil {
		panic(fmt.Errorf("sqlite: %v (%s)", err, q))
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []*pyjson.Map
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			panic(err)
		}
		m := pyjson.NewMap()
		for i, c := range cols {
			v := vals[i]
			switch t := v.(type) {
			case []byte:
				v = string(t)
			case int:
				v = int64(t)
			case bool:
				if t {
					v = int64(1)
				} else {
					v = int64(0)
				}
			}
			m.Set(c, v)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		panic(err)
	}
	return out
}

// QueryOne returns the first row or nil.
func (x *DB) QueryOne(q string, args ...any) *pyjson.Map {
	r := x.Query(q, args...)
	if len(r) == 0 {
		return nil
	}
	return r[0]
}
