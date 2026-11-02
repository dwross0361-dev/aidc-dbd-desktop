package estate

import "strings"

// CSVWriter writes rows as Python's csv.writer does (QUOTE_MINIMAL, "\r\n", None as "").
type CSVWriter struct{ b strings.Builder }

// Row writes one row.
func (w *CSVWriter) Row(fields ...any) {
	for i, f := range fields {
		if i > 0 {
			w.b.WriteByte(',')
		}
		s := ""
		if f != nil {
			s = PyStr(f)
		}
		if strings.ContainsAny(s, ",\"\r\n") || (s == "" && len(fields) == 1) {
			w.b.WriteString(`"` + strings.ReplaceAll(s, `"`, `""`) + `"`)
		} else {
			w.b.WriteString(s)
		}
	}
	w.b.WriteString("\r\n")
}

func (w *CSVWriter) String() string { return w.b.String() }
