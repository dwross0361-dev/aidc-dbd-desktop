// Package pyjson reads and writes JSON the way Python's json module does, so state written by either engine is
// byte-identical: objects keep their key order, integers and floats stay distinct (1 vs 1.0), floats use Python's
// repr, output is ASCII-escaped, and the separators are Python's defaults.
//
// Values are: nil, bool, int64, float64, string, []any, *Map.
package pyjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Map is a JSON object that keeps insertion order (a Python dict).
type Map struct {
	keys []string
	m    map[string]any
}

// NewMap returns an empty Map.
func NewMap() *Map { return &Map{m: map[string]any{}} }

// O builds a Map from alternating keys and values.
func O(kv ...any) *Map {
	o := NewMap()
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// Get returns the value for k.
func (o *Map) Get(k string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.m[k]
	return v, ok
}

// V returns the value for k, or nil.
func (o *Map) V(k string) any {
	if o == nil {
		return nil
	}
	return o.m[k]
}

// Has reports whether k is present.
func (o *Map) Has(k string) bool {
	if o == nil {
		return false
	}
	_, ok := o.m[k]
	return ok
}

// Set sets k (a new key goes last; an existing key keeps its position).
func (o *Map) Set(k string, v any) {
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = v
}

// SetDefault sets k only when absent and returns the value held.
func (o *Map) SetDefault(k string, v any) any {
	if cur, ok := o.m[k]; ok {
		return cur
	}
	o.Set(k, v)
	return v
}

// Delete removes k.
func (o *Map) Delete(k string) {
	if _, ok := o.m[k]; !ok {
		return
	}
	delete(o.m, k)
	for i, x := range o.keys {
		if x == k {
			o.keys = append(o.keys[:i:i], o.keys[i+1:]...)
			break
		}
	}
}

// Keys returns the keys in order.
func (o *Map) Keys() []string {
	if o == nil {
		return nil
	}
	return o.keys
}

// Len returns the number of keys.
func (o *Map) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Update sets every key of other, in its order (dict.update).
func (o *Map) Update(other *Map) {
	if other == nil {
		return
	}
	for _, k := range other.keys {
		o.Set(k, other.m[k])
	}
}

// Copy returns a shallow copy.
func (o *Map) Copy() *Map {
	n := NewMap()
	if o != nil {
		n.Update(o)
	}
	return n
}

// Str returns the string at k, or "".
func (o *Map) Str(k string) string { return Str(o.V(k)) }

// Int returns the number at k as int64 (floats truncate), or 0.
func (o *Map) Int(k string) int64 { return Int(o.V(k)) }

// Float returns the number at k, or 0.
func (o *Map) Float(k string) float64 { return Float(o.V(k)) }

// M returns the object at k, or nil.
func (o *Map) M(k string) *Map { m, _ := o.V(k).(*Map); return m }

// L returns the list at k, or nil.
func (o *Map) L(k string) []any { l, _ := o.V(k).([]any); return l }

// Truthy is Python truthiness of the value at k.
func (o *Map) Truthy(k string) bool { return Truthy(o.V(k)) }

// DeepCopy copies maps and lists recursively.
func DeepCopy(v any) any {
	switch x := v.(type) {
	case *Map:
		n := NewMap()
		for _, k := range x.keys {
			n.Set(k, DeepCopy(x.m[k]))
		}
		return n
	case []any:
		n := make([]any, len(x))
		for i := range x {
			n[i] = DeepCopy(x[i])
		}
		return n
	}
	return v
}

// Str returns v if it is a string, else "".
func Str(v any) string { s, _ := v.(string); return s }

// Int returns v as int64.
func Int(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case bool:
		if x {
			return 1
		}
	}
	return 0
}

// Float returns v as float64.
func Float(v any) float64 {
	switch x := v.(type) {
	case int64:
		return float64(x)
	case int:
		return float64(x)
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
	}
	return 0
}

// IsNum reports whether v is a number (bool excluded).
func IsNum(v any) bool {
	switch v.(type) {
	case int64, int, float64:
		return true
	}
	return false
}

// Truthy is Python truthiness.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case int64:
		return x != 0
	case int:
		return x != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case []string:
		return len(x) > 0
	case *Map:
		return x != nil && len(x.keys) > 0
	}
	return true
}

// StrList returns the strings of a list value.
func StrList(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, x := range l {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// ------------------------------------------------------------------------------------------------ decoding

// Loads parses JSON text as Python's json.loads does (integers vs floats kept apart, key order kept).
func Loads(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("extra data after JSON value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return decodeTok(dec, t)
}

func decodeTok(dec *json.Decoder, t json.Token) (any, error) {
	switch x := t.(type) {
	case json.Delim:
		switch x {
		case '{':
			o := NewMap()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string")
				}
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				o.Set(k, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			l := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				l = append(l, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return l, nil
		}
		return nil, fmt.Errorf("unexpected delimiter %v", x)
	case json.Number:
		s := string(x)
		if strings.ContainsAny(s, ".eE") {
			f, err := strconv.ParseFloat(s, 64)
			return f, err
		}
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i, nil
		}
		f, err := strconv.ParseFloat(s, 64)
		return f, err
	case string, bool, nil:
		return x, nil
	}
	return nil, fmt.Errorf("unexpected token %v", t)
}

// LoadsMap parses a JSON object.
func LoadsMap(data []byte) (*Map, error) {
	v, err := Loads(data)
	if err != nil {
		return nil, err
	}
	m, ok := v.(*Map)
	if !ok {
		return nil, fmt.Errorf("JSON value is not an object")
	}
	return m, nil
}

// ------------------------------------------------------------------------------------------------ encoding

// Dumps is json.dumps(v): separators ", " and ": ", ASCII only.
func Dumps(v any) string {
	var b strings.Builder
	enc(&b, v, -1, 0)
	return b.String()
}

// DumpsIndent is json.dumps(v, indent=n) (separators "," and ": ").
func DumpsIndent(v any, n int) string {
	var b strings.Builder
	enc(&b, v, n, 0)
	return b.String()
}

func nl(b *strings.Builder, indent, level int) {
	b.WriteByte('\n')
	b.WriteString(strings.Repeat(" ", indent*level))
}

func enc(b *strings.Builder, v any, indent, level int) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case int:
		b.WriteString(strconv.Itoa(x))
	case float64:
		b.WriteString(FloatRepr(x))
	case string:
		Quote(b, x)
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				if indent >= 0 {
					b.WriteByte(',')
				} else {
					b.WriteString(", ")
				}
			}
			if indent >= 0 {
				nl(b, indent, level+1)
			}
			enc(b, e, indent, level+1)
		}
		if indent >= 0 {
			nl(b, indent, level)
		}
		b.WriteByte(']')
	case []string:
		l := make([]any, len(x))
		for i := range x {
			l[i] = x[i]
		}
		enc(b, l, indent, level)
	case []*Map:
		l := make([]any, len(x))
		for i := range x {
			l[i] = x[i]
		}
		enc(b, l, indent, level)
	case *Map:
		if x == nil {
			b.WriteString("null")
			return
		}
		if len(x.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteByte('{')
		for i, k := range x.keys {
			if i > 0 {
				if indent >= 0 {
					b.WriteByte(',')
				} else {
					b.WriteString(", ")
				}
			}
			if indent >= 0 {
				nl(b, indent, level+1)
			}
			Quote(b, k)
			b.WriteString(": ")
			enc(b, x.m[k], indent, level+1)
		}
		if indent >= 0 {
			nl(b, indent, level)
		}
		b.WriteByte('}')
	case map[string]any: // sorted keys; only for values built in Go where order is not observable
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		o := NewMap()
		for _, k := range keys {
			o.Set(k, x[k])
		}
		enc(b, o, indent, level)
	default:
		Quote(b, fmt.Sprint(x))
	}
}

// Quote writes s as a JSON string with Python's ensure_ascii escaping.
func Quote(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// Python strings from json.loads are valid; a stray byte is written as its code point
			r = rune(s[i])
		}
		i += size
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r >= 0x20 && r <= 0x7e {
				b.WriteRune(r)
			} else if r > 0xffff {
				r1, r2 := utf16.EncodeRune(r)
				fmt.Fprintf(b, `\u%04x\u%04x`, r1, r2)
			} else {
				fmt.Fprintf(b, `\u%04x`, r)
			}
		}
	}
	b.WriteByte('"')
}

// FloatRepr is Python's repr(float).
func FloatRepr(f float64) string {
	if math.IsInf(f, 1) {
		return "Infinity"
	}
	if math.IsInf(f, -1) {
		return "-Infinity"
	}
	if math.IsNaN(f) {
		return "NaN"
	}
	if f == 0 {
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // d.ddddde±XX, shortest round-trip digits
	neg := false
	if e[0] == '-' {
		neg = true
		e = e[1:]
	}
	ei := strings.IndexByte(e, 'e')
	mant, expS := e[:ei], e[ei+1:]
	exp, _ := strconv.Atoi(expS)
	digits := strings.Replace(mant, ".", "", 1)
	var out string
	if exp < -4 || exp >= 16 {
		m := digits[:1]
		if len(digits) > 1 {
			m += "." + digits[1:]
		}
		sign := "+"
		if exp < 0 {
			sign = "-"
			exp = -exp
		}
		out = fmt.Sprintf("%se%s%02d", m, sign, exp)
	} else if exp < 0 {
		out = "0." + strings.Repeat("0", -exp-1) + digits
	} else if exp+1 >= len(digits) {
		out = digits + strings.Repeat("0", exp+1-len(digits)) + ".0"
	} else {
		out = digits[:exp+1] + "." + digits[exp+1:]
	}
	if neg {
		return "-" + out
	}
	return out
}

// Canon is json.dumps(v, sort_keys=True, separators=(",", ":")): the canonical form a report's SHA-256 is taken over.
func Canon(v any) string {
	var b strings.Builder
	canon(&b, v)
	return b.String()
}

func canon(b *strings.Builder, v any) {
	switch x := v.(type) {
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			canon(b, e)
		}
		b.WriteByte(']')
	case []string:
		l := make([]any, len(x))
		for i := range x {
			l[i] = x[i]
		}
		canon(b, l)
	case []*Map:
		l := make([]any, len(x))
		for i := range x {
			l[i] = x[i]
		}
		canon(b, l)
	case *Map:
		if x == nil {
			b.WriteString("null")
			return
		}
		keys := append([]string(nil), x.keys...)
		sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			Quote(b, k)
			b.WriteByte(':')
			canon(b, x.m[k])
		}
		b.WriteByte('}')
	case map[string]any:
		o := NewMap()
		for k, e := range x {
			o.Set(k, e)
		}
		canon(b, o)
	default:
		enc(b, v, -1, 0)
	}
}

// utf16Less orders strings as Python compares str (by code point).
func utf16Less(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	for i := 0; i < len(ra) && i < len(rb); i++ {
		if ra[i] != rb[i] {
			return ra[i] < rb[i]
		}
	}
	return len(ra) < len(rb)
}
