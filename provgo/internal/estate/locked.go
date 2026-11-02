package estate

// WithLock runs f under the estate lock (for the HTTP handlers' multi-step checks).
func (e *Estate) WithLock(f func()) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	f()
}

// EventL logs an event; the lock must be held.
func (e *Estate) EventL(text, level, device string) { e.ev(text, level, device) }

// FailL fails a device; the lock must be held.
func (e *Estate) FailL(n, detail string) { e.fail(n, detail) }

// BuildOf is the build key of a device's wave, "" when it is in none.
func (e *Estate) BuildOf(n string) string { return e.WaveOf[n].build }

// RunState is the run's state.
func (e *Estate) RunState() string {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	return e.Run.Str("state")
}

// RandHex is secrets.token_hex(n).
func RandHex(n int) string { return tokenHex(n) }

// RoleNow is the host's role (primary or standby).
func (e *Estate) RoleNow() string { return e.roleNow() }

// PyInt is Python int() of a string.
func PyInt(s string) (int64, error) { return pyInt(s) }
