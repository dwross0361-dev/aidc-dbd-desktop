package estate

import (
	"bufio"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aidc-dbd/provgo/internal/pyjson"
)

// STG is the stage model, index = stage number.
var STG = []string{"Expected", "DHCP", "Bootstrap", "Image", "Config", "Verified", "Done"}

var (
	TokenRE   = regexp.MustCompile(`<([A-Z][A-Za-z0-9_. &-]*)>`)
	SerialRE  = regexp.MustCompile(`^[A-Z0-9][A-Z0-9._/-]{2,63}$`)
	KeaTextRE = regexp.MustCompile(`^[A-Za-z0-9 ._/:+-]*$`)
	EnvKeyRE  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	skipToken = regexp.MustCompile(`(^|/)(README[^/]*|[^/]*\.md|[^/]*\.csv)$`)
	nonHex    = regexp.MustCompile(`[^0-9A-Fa-f]`)
	vcRE      = regexp.MustCompile(`^Juniper-([A-Za-z0-9-]+?)-([A-Za-z0-9]+)$`)
	portPfx   = regexp.MustCompile(`^(ethernet|eth|et-|ge-|xe-|mge-|swp|e)`)
	fileSafe  = regexp.MustCompile(`[^A-Za-z0-9_-]`)
)

// TokenDirs are the web-root directories whose files are scanned for unfilled tokens.
var TokenDirs = []string{"bootstrap", "configs", "ztp", "tftp", "eos", "srl", "sros", "hbn", "xr"}

// Now is time.time().
func Now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// ISO is the Python iso(): UTC seconds, "" for 0.
func ISO(ts float64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(int64(ts), 0).UTC().Format("2006-01-02T15:04:05Z")
}

func isoAny(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return ISO(pyjson.Float(v))
}

// NormMAC returns aa:bb:cc:dd:ee:ff, or "" unless there are exactly 12 hex digits.
func NormMAC(s string) string {
	h := strings.ToLower(nonHex.ReplaceAllString(s, ""))
	if len(h) != 12 {
		return ""
	}
	parts := make([]string, 6)
	for i := 0; i < 6; i++ {
		parts[i] = h[2*i : 2*i+2]
	}
	return strings.Join(parts, ":")
}

// NormMACAny is norm_mac(str(s or "")).
func NormMACAny(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return NormMAC(PyStr(v))
}

// NormFP returns a SHA-256 fingerprint as 64 lower-case hex digits, or "".
func NormFP(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			b.WriteRune(c)
		}
	}
	if b.Len() != 64 {
		return ""
	}
	return b.String()
}

// CanonRel is canon_rel: the canonical web-root name of a decoded request path, ok=false when refused.
func CanonRel(path string) (string, bool) {
	rel := strings.TrimLeft(path, "/")
	if rel == "" || strings.Contains(rel, "\x00") {
		return "", false
	}
	segs := strings.Split(rel, "/")
	var keep []string
	for _, x := range segs {
		if x == "" || x == ".." {
			return "", false
		}
		if x != "." {
			keep = append(keep, x)
		}
	}
	if len(keep) == 0 {
		return "", false
	}
	return strings.Join(keep, "/"), true
}

// HexToBytes is hex_to_bytes.
func HexToBytes(v string) []byte {
	s := strings.TrimSpace(v)
	if strings.HasPrefix(strings.ToLower(s), "0x") {
		s = s[2:]
	}
	s = strings.ReplaceAll(strings.ReplaceAll(s, ":", ""), " ", "")
	if s == "" || len(s)%2 != 0 {
		return nil
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

// Printable returns the bytes as text when every byte is printable ASCII, else "".
func Printable(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	for _, c := range b {
		if c < 32 || c >= 127 {
			return ""
		}
	}
	return string(b)
}

// pyStrip is str.strip() for ASCII whitespace.
func pyStrip(s string) string { return strings.Trim(s, " \t\n\r\x0b\x0c") }

// SerialFromClientID: Nokia (type 0 + serial) and NX-OS POAP send the chassis serial as option 61.
func SerialFromClientID(cid string) string {
	b := HexToBytes(cid)
	if len(b) == 0 {
		return ""
	}
	if b[0] == 0 {
		return strings.ToUpper(pyStrip(Printable(b[1:])))
	}
	if b[0] == 1 {
		return ""
	}
	return strings.ToUpper(pyStrip(Printable(b)))
}

// SerialFromVendorClass returns (serial, model) from Juniper-<model>-<serial>.
func SerialFromVendorClass(vc string) (string, string) {
	m := vcRE.FindStringSubmatch(vc)
	if m == nil {
		return "", ""
	}
	return strings.ToUpper(m[2]), strings.ToLower(m[1])
}

// NormPort is norm_port.
func NormPort(p string) string {
	s := portPfx.ReplaceAllString(strings.ToLower(pyStrip(p)), "")
	s = strings.ReplaceAll(s, "/", ":")
	return strings.SplitN(s, ".", 2)[0]
}

// LoadEnvFile reads KEY=VALUE lines.
func LoadEnvFile(path string) map[string]string {
	vals := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return vals
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		ln := pyStrip(sc.Text())
		if ln == "" || strings.HasPrefix(ln, "#") || !strings.Contains(ln, "=") {
			continue
		}
		kv := strings.SplitN(ln, "=", 2)
		k, v := pyStrip(kv[0]), pyStrip(kv[1])
		if len(v) >= 2 && v[0] == v[len(v)-1] && (v[0] == '"' || v[0] == '\'') {
			v = v[1 : len(v)-1]
		}
		vals[k] = v
	}
	return vals
}

// ------------------------------------------------------------------------------------------------ passwords

// PBKDF2 hashes as pbkdf2$<iters>$<salt>$<hex>; salt "" draws 16 random bytes as 32 hex characters.
func PBKDF2(password, salt string, iters int) string {
	if salt == "" {
		salt = tokenHex(16)
	}
	k, _ := pbkdf2.Key(sha256.New, password, []byte(salt), iters, 32)
	return fmt.Sprintf("pbkdf2$%d$%s$%s", iters, salt, hex.EncodeToString(k))
}

// DummyPW is hashed in place of a missing user's entry.
var DummyPW = "pbkdf2$240000$" + strings.Repeat("0", 32) + "$" + strings.Repeat("0", 64)

// PBKDF2OK verifies a password against a stored hash.
func PBKDF2OK(password, stored string) bool {
	p := strings.Split(stored, "$")
	if len(p) != 4 {
		return false
	}
	iters, err := pyInt(p[1])
	if err != nil || iters <= 0 {
		return false
	}
	got := strings.Split(PBKDF2(password, p[2], int(iters)), "$")[3]
	return hmac.Equal([]byte(got), []byte(p[3]))
}

// TempPWChars has no l/I/1, O/0, -/_.
const TempPWChars = "ABCDEFGHJKMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789"

// TempPassword returns n characters from TempPWChars.
func TempPassword(n int) string {
	b := make([]byte, n)
	max := big.NewInt(int64(len(TempPWChars)))
	for i := range b {
		x, _ := rand.Int(rand.Reader, max)
		b[i] = TempPWChars[x.Int64()]
	}
	return string(b)
}

func tokenHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func tokenURLSafe(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ------------------------------------------------------------------------------------------------ Python formatting

// PyStr is Python str() of a JSON-ish value.
func PyStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case float64:
		return pyjson.FloatRepr(x)
	}
	return PyRepr(v)
}

// PyRepr is Python repr() of a JSON-ish value.
func PyRepr(v any) string {
	switch x := v.(type) {
	case string:
		return pyStrRepr(x)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = PyRepr(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case []string:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = pyStrRepr(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *pyjson.Map:
		parts := []string{}
		for _, k := range x.Keys() {
			parts = append(parts, pyStrRepr(k)+": "+PyRepr(x.V(k)))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return PyStr(v)
}

func pyStrRepr(s string) string {
	q := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, "\"") {
		q = "\""
	}
	var b strings.Builder
	b.WriteString(q)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case string(r) == q:
			b.WriteString(`\` + q)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 32 || r == 127:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteString(q)
	return b.String()
}

// pyD is Python "%d" of a number (floats truncate toward zero).
func pyD(f float64) string { return strconv.FormatInt(int64(f), 10) }

// pyInt is Python int(str): surrounding whitespace, a sign, digits with single underscores between them.
func pyInt(s string) (int64, error) {
	t := pyStrip(s)
	neg := false
	if t != "" && (t[0] == '+' || t[0] == '-') {
		neg = t[0] == '-'
		t = t[1:]
	}
	if t == "" || t[0] == '_' || t[len(t)-1] == '_' || strings.Contains(t, "__") {
		return 0, errors.New("invalid literal")
	}
	t = strings.ReplaceAll(t, "_", "")
	for _, c := range t {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid literal")
		}
	}
	n, err := strconv.ParseInt(t, 10, 64)
	if err != nil {
		return 0, err
	}
	if neg {
		n = -n
	}
	return n, nil
}

// pyErrno renders an OS error the way Python's OSError str() does: "[Errno N] Message[: 'file'[ -> 'file2']]".
func pyErrno(err error, files ...string) string {
	var pe *fs.PathError
	var le *os.LinkError
	var se *os.SyscallError
	switch {
	case errors.As(err, &le):
		err = le.Err
		if len(files) == 0 {
			files = []string{le.Old, le.New}
		}
	case errors.As(err, &pe):
		err = pe.Err
		if len(files) == 0 {
			files = []string{pe.Path}
		}
	case errors.As(err, &se):
		err = se.Err
	}
	var en syscall.Errno
	if errors.As(err, &en) {
		msg := en.Error()
		if msg != "" {
			msg = strings.ToUpper(msg[:1]) + msg[1:]
		}
		s := fmt.Sprintf("[Errno %d] %s", int(en), msg)
		if len(files) > 0 {
			s += ": " + pyStrRepr(files[0])
			if len(files) > 1 {
				s += " -> " + pyStrRepr(files[1])
			}
		}
		return s
	}
	if os.IsTimeout(err) {
		return "timed out"
	}
	return err.Error()
}

func safeName(n string) string { return fileSafe.ReplaceAllString(n, "-") }

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func sortedStrings(l []string) []string {
	c := append([]string(nil), l...)
	sort.Strings(c)
	return c
}

// FileHasTokensText scans text the way file_has_tokens does (comment lines skipped), first 3 tokens.
func FileHasTokensText(text string) []string {
	var out []string
	lines := strings.SplitAfter(text, "\n")
	for _, ln := range lines {
		if ln == "" {
			continue
		}
		t := strings.TrimLeft(ln, " \t\n\r\x0b\x0c")
		if strings.HasPrefix(t, "#") || strings.HasPrefix(t, "!") || strings.HasPrefix(t, "//") {
			continue
		}
		for _, m := range TokenRE.FindAllStringSubmatch(ln, -1) {
			out = append(out, m[1])
			if len(out) >= 3 {
				return out
			}
		}
	}
	return out
}

// TokenScanApplies reports whether rel is in a token directory and not skipped.
func TokenScanApplies(rel string) bool {
	return containsStr(TokenDirs, strings.SplitN(rel, "/", 2)[0]) && !skipToken.MatchString(rel)
}
