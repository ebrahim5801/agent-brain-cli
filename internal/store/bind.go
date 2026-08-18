package store

// normalizeArgs bridges the one type gap between the drivers: SQLite silently
// coerces a Go bool to its 0/1 INTEGER columns, but pgx refuses to bind a bool
// to a BIGINT column. The schema keeps these flags as integers (they are
// compared to 0/1 in SQL), so every bind converts bool→int64. Harmless on
// SQLite; required on Postgres. Applied in the Exec/Query/QueryRow funnel.
func normalizeArgs(args []any) []any {
	for i, a := range args {
		switch v := a.(type) {
		case bool:
			args[i] = b2i(v)
		case *bool:
			// defensive: callers pass values, not pointers, but normalize anyway
			if v != nil {
				args[i] = b2i(*v)
			}
		}
	}
	return args
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// intBool scans a 0/1 flag column into a bool across both backends: SQLite
// returns int64 (or occasionally []byte); Postgres returns int64 for a BIGINT
// column. It is the read-side counterpart to normalizeArgs.
type intBool bool

func (b *intBool) Scan(v any) error {
	switch t := v.(type) {
	case nil:
		*b = false
	case int64:
		*b = t != 0
	case bool:
		*b = intBool(t)
	case []byte:
		*b = len(t) == 1 && t[0] == '1'
	case string:
		*b = t == "1" || t == "true"
	default:
		*b = false
	}
	return nil
}
