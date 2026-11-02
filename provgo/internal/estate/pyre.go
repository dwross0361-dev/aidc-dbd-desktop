package estate

import (
	"math"

	"aidc-dbd/provgo/internal/pyjson"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Python's re module (str patterns) treats \s, \d and \w as Unicode classes; RE2 treats them as ASCII. pyRe rewrites a
// Python pattern so these classes match what Python matches on device output with non-ASCII characters. (\b stays ASCII
// in RE2: a word boundary next to a non-ASCII letter can differ; device output keeps this to descriptions.)
const (
	pySpace    = `\t\n\v\f\r \x{1c}-\x{1f}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`
	pyDigit    = `\p{Nd}`
	pyWordBody = `\p{L}\p{N}_`
)

func pyRe(p string) *regexp.Regexp { return regexp.MustCompile(pyReSrc(p)) }

func pyReSrc(p string) string {
	var b strings.Builder
	inClass := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '\\' && i+1 < len(p) {
			n := p[i+1]
			i++
			switch n {
			case 's':
				if inClass {
					b.WriteString(pySpace)
				} else {
					b.WriteString("[" + pySpace + "]")
				}
			case 'S':
				if inClass {
					// a negated class inside a class: RE2 cannot nest; Python patterns here never do this
					b.WriteString(`\S`)
				} else {
					b.WriteString("[^" + pySpace + "]")
				}
			case 'd':
				b.WriteString(pyDigit)
			case 'D':
				if inClass {
					b.WriteString(`\D`)
				} else {
					b.WriteString(`\P{Nd}`)
				}
			case 'w':
				if inClass {
					b.WriteString(pyWordBody)
				} else {
					b.WriteString("[" + pyWordBody + "]")
				}
			case 'W':
				if inClass {
					b.WriteString(`\W`)
				} else {
					b.WriteString("[^" + pyWordBody + "]")
				}
			default:
				b.WriteByte('\\')
				b.WriteByte(n)
			}
			continue
		}
		if c == '[' && !inClass {
			inClass = true
			b.WriteByte(c)
			// a leading ^ or ] belongs to the class
			if i+1 < len(p) && p[i+1] == '^' {
				b.WriteByte('^')
				i++
			}
			if i+1 < len(p) && p[i+1] == ']' {
				b.WriteString(`\]`)
				i++
			}
			continue
		}
		if c == ']' && inClass {
			inClass = false
		}
		b.WriteByte(c)
	}
	return b.String()
}

// pyIsSpace is str.isspace() for one character.
func pyIsSpace(r rune) bool {
	switch {
	case r >= 0x09 && r <= 0x0d, r >= 0x1c && r <= 0x20, r == 0x85, r == 0xa0, r == 0x1680, r >= 0x2000 && r <= 0x200a,
		r == 0x2028, r == 0x2029, r == 0x202f, r == 0x205f, r == 0x3000:
		return true
	}
	return false
}

// pyStripU is str.strip() (Unicode whitespace).
func pyStripU(s string) string { return strings.TrimFunc(s, pyIsSpace) }

// pySplitlines is str.splitlines().
func pySplitlines(s string) []string {
	var out []string
	start := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch r {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, string(rs[start:i]))
			start = i + 1
		case '\r':
			out = append(out, string(rs[start:i]))
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}

// pyLower is str.lower() (simple mapping; Python's full mapping differs only for a handful of characters).
func pyLower(s string) string { return strings.ToLower(s) }

// pyTail is s[-n:] in characters.
func pyTail(s string, n int) string {
	if len(s) <= n { // fewer bytes than n: fewer characters too
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// pyHead is s[:n] in characters.
func pyHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func runeLen(s string) int { return len([]rune(s)) }

// pyFloat is float(str): surrounding whitespace, sign, digits with single underscores between digits, a fraction and
// an exponent; nan, inf and infinity in any case.
func pyFloat(s string) (float64, bool) {
	t := pyStripU(s)
	if t == "" {
		return 0, false
	}
	low := strings.ToLower(t)
	sign := 1.0
	body := low
	if body[0] == '+' || body[0] == '-' {
		if body[0] == '-' {
			sign = -1
		}
		body = body[1:]
	}
	switch body {
	case "nan":
		return math.NaN(), true
	case "inf", "infinity":
		return math.Inf(int(sign)), true
	}
	// validate: digits with single underscores between digits
	clean := make([]byte, 0, len(t))
	prevDigit := false
	for i := 0; i < len(t); i++ {
		c := t[i]
		if c == '_' {
			if !prevDigit || i+1 >= len(t) || t[i+1] < '0' || t[i+1] > '9' {
				return 0, false
			}
			prevDigit = false
			continue
		}
		if c > 127 {
			// Unicode digits are accepted by Python; map the decimal ones
			r := []rune(t[i:])[0]
			if unicode.IsDigit(r) {
				clean = append(clean, byte('0'+digitVal(r)))
				i += len(string(r)) - 1
				prevDigit = true
				continue
			}
			return 0, false
		}
		prevDigit = c >= '0' && c <= '9'
		clean = append(clean, c)
	}
	cs := string(clean)
	lc := strings.ToLower(cs)
	if strings.ContainsAny(lc, "xp") || strings.Contains(lc, "inf") || strings.Contains(lc, "nan") {
		return 0, false
	}
	f, err := strconv.ParseFloat(cs, 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return f, true // Python: overflow to inf for float("1e999")
		}
		return 0, false
	}
	return f, true
}

func digitVal(r rune) int {
	for _, rt := range unicode.Nd.R16 {
		if uint16(r) >= rt.Lo && uint16(r) <= rt.Hi && r <= 0xffff {
			return int(uint16(r)-rt.Lo) % 10
		}
	}
	for _, rt := range unicode.Nd.R32 {
		if uint32(r) >= rt.Lo && uint32(r) <= rt.Hi {
			return int(uint32(r)-rt.Lo) % 10
		}
	}
	return 0
}

// pyPctG is Python "%g".
func pyPctG(x float64) string {
	if math.IsInf(x, 1) {
		return "inf"
	}
	if math.IsInf(x, -1) {
		return "-inf"
	}
	if math.IsNaN(x) {
		return "nan"
	}
	s := strconv.FormatFloat(x, 'g', 6, 64)
	// Go writes e+06; Python writes e+06 as well, but trims zeros the same way; Go may write "1e+06" vs Python "1e+06"
	return s
}

// pyPctF is Python "%.Nf".
func pyPctF(x float64, n int) string {
	if math.IsInf(x, 1) {
		return "inf"
	}
	if math.IsInf(x, -1) {
		return "-inf"
	}
	if math.IsNaN(x) {
		return "nan"
	}
	return strconv.FormatFloat(x, 'f', n, 64)
}

// pyPctD is Python "%d" of a float (truncation toward zero).
func pyPctD(x float64) string {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return pyPctF(x, 0) // Python raises; never reached with real counters
	}
	if math.Abs(x) < 9e18 {
		return strconv.FormatInt(int64(x), 10)
	}
	return strconv.FormatFloat(math.Trunc(x), 'f', 0, 64)
}

// pyFloatStr is str(float).
func pyFloatStr(x float64) string {
	switch {
	case math.IsInf(x, 1):
		return "inf"
	case math.IsInf(x, -1):
		return "-inf"
	case math.IsNaN(x):
		return "nan"
	}
	return pyjson.FloatRepr(x)
}

// PyFloat is float(str) for the CLI.
func PyFloat(s string) (float64, bool) { return pyFloat(s) }
