package store

import "strings"

// rebind rewrites a query written in SQLite's `?` placeholder style into the
// backend's native style. For SQLite it is the identity. For Postgres each `?`
// becomes `$1, $2, ...` in order. Single-quoted string literals are skipped so
// a literal apostrophe or a `?` inside quotes is never mistaken for a
// placeholder (a guard test asserts no local query relies on `?` in a literal,
// so the second case should never occur — but the scanner stays correct if it
// ever does).
func rebind(backend Backend, query string) string {
	if backend != BackendPostgres {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	inLiteral := false
	for i := 0; i < len(query); i++ {
		c := query[i]
		if inLiteral {
			b.WriteByte(c)
			if c == '\'' {
				// A doubled '' is an escaped quote inside the literal, not its end.
				if i+1 < len(query) && query[i+1] == '\'' {
					b.WriteByte('\'')
					i++
					continue
				}
				inLiteral = false
			}
			continue
		}
		switch c {
		case '\'':
			inLiteral = true
			b.WriteByte(c)
		case '?':
			n++
			b.WriteByte('$')
			b.WriteString(itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// itoa avoids strconv for a hot, tiny-integer path.
func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
