package parsers

import "strings"

// sqlOpTarget derives a stable {operation, target} pair from a SQL statement.
//
// It is deliberately a keyword scan rather than a parser: the pair only has to
// be *identical across the three shadow roles running the same code path*, not
// semantically complete. A statement it cannot classify still reports, with an
// empty target, and diffs on its payload instead of its signature.
//
// ponytail: naive keyword scan. Ceiling — CTEs (`WITH x AS (...) SELECT`) report
// the target of the first FROM found, and multi-table joins report only the
// first table. Both are stable across roles, which is the property that matters.
// Upgrade path is a real parser (e.g. vitess/sqlparser) if signatures ever prove
// too coarse to localise a regression.
func sqlOpTarget(sql string) (operation, target string) {
	fields := strings.Fields(stripSQLComments(sql))
	if len(fields) == 0 {
		return "", ""
	}
	operation = strings.ToLower(strings.Trim(fields[0], "(;"))

	// The keyword that introduces the table depends on the verb.
	var want string
	switch operation {
	case "select", "delete":
		want = "from"
	case "insert", "replace":
		want = "into"
	case "update":
		// UPDATE <table> SET ... — the table is the next token.
		if len(fields) > 1 {
			return operation, normalizeIdent(fields[1])
		}
		return operation, ""
	case "exec", "execute", "call":
		if len(fields) > 1 {
			return operation, normalizeIdent(fields[1])
		}
		return operation, ""
	default:
		return operation, ""
	}

	for i := 0; i < len(fields)-1; i++ {
		if strings.EqualFold(fields[i], want) {
			return operation, normalizeIdent(fields[i+1])
		}
	}
	return operation, ""
}

// normalizeIdent trims the punctuation and quoting styles a table name can carry
// so `users`, `"users"`, `[users]`, `users,` and `public.users` do not produce
// four different signatures for one table.
func normalizeIdent(s string) string {
	s = strings.Trim(s, "\"`[]();,")
	if i := strings.LastIndex(s, "."); i >= 0 && i < len(s)-1 {
		s = s[i+1:]
	}
	return strings.ToLower(strings.Trim(s, "\"`[]"))
}

// stripSQLComments removes `/* ... */` and `-- ...` so a traceparent comment
// injected by sqlcommenter cannot be mistaken for a table name. The trace id is
// extracted from the raw bytes before this runs.
func stripSQLComments(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))
	for i := 0; i < len(sql); {
		switch {
		case strings.HasPrefix(sql[i:], "/*"):
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				return b.String()
			}
			b.WriteByte(' ')
			i += 2 + end + 2
		case strings.HasPrefix(sql[i:], "--"):
			end := strings.IndexByte(sql[i:], '\n')
			if end < 0 {
				return b.String()
			}
			b.WriteByte(' ')
			i += end + 1
		default:
			b.WriteByte(sql[i])
			i++
		}
	}
	return b.String()
}
