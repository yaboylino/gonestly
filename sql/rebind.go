package sql

import (
	"database/sql/driver"
	"fmt"
	"reflect"
	"strings"
)

// rebind rewrites the ? placeholders of a query for the dialect and expands slice arguments,
// so "id IN (?)" with []int{1, 2} becomes "id IN ($1, $2)". It skips quoted text and comments,
// and "??" stands for a literal question mark, such as the jsonb operator of PostgreSQL.
func rebind(d Dialect, query string, args []any) (string, []any, error) {
	var b strings.Builder
	b.Grow(len(query) + 8)
	out := make([]any, 0, len(args))
	next := 0

	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case c == '\'' || c == '"':
			end := closingQuote(query, i)
			b.WriteString(query[i:end])
			i = end - 1
		case c == '-' && i+1 < len(query) && query[i+1] == '-':
			end := strings.IndexByte(query[i:], '\n')
			if end < 0 {
				end = len(query) - i
			}
			b.WriteString(query[i : i+end])
			i += end - 1
		case c == '/' && i+1 < len(query) && query[i+1] == '*':
			end := strings.Index(query[i+2:], "*/")
			if end < 0 {
				b.WriteString(query[i:])
				i = len(query)
				continue
			}
			b.WriteString(query[i : i+end+4])
			i += end + 3
		case c == '$' && d == Postgres:
			if end, ok := dollarQuoteEnd(query, i); ok {
				b.WriteString(query[i:end])
				i = end - 1
				continue
			}
			b.WriteByte(c)
		case c == '?' && i+1 < len(query) && query[i+1] == '?':
			b.WriteByte('?')
			i++
		case c == '?':
			if next >= len(args) {
				return "", nil, fmt.Errorf("sql: the query has more placeholders than the %d arguments", len(args))
			}
			values, err := expand(args[next], next+1)
			if err != nil {
				return "", nil, err
			}
			for j, v := range values {
				if j > 0 {
					b.WriteString(", ")
				}
				out = append(out, v)
				b.WriteString(d.Placeholder(len(out)))
			}
			next++
		default:
			b.WriteByte(c)
		}
	}
	if next != len(args) {
		return "", nil, fmt.Errorf("sql: the query has %d placeholders but %d arguments", next, len(args))
	}
	return b.String(), out, nil
}

// expand returns the elements of a slice argument, or the argument itself.
func expand(arg any, position int) ([]any, error) {
	if l, ok := arg.(literal); ok {
		return []any{l.value}, nil
	}
	if _, ok := arg.(driver.Valuer); ok || arg == nil {
		return []any{arg}, nil
	}
	v := reflect.ValueOf(arg)
	if v.Kind() != reflect.Slice || v.Type().Elem().Kind() == reflect.Uint8 {
		return []any{arg}, nil
	}
	if v.Len() == 0 {
		return nil, fmt.Errorf("sql: argument %d is an empty slice; an IN list needs at least one value", position)
	}
	values := make([]any, v.Len())
	for i := range values {
		values[i] = v.Index(i).Interface()
	}
	return values, nil
}

func closingQuote(query string, start int) int {
	quote := query[start]
	for i := start + 1; i < len(query); i++ {
		if query[i] == quote {
			if i+1 < len(query) && query[i+1] == quote {
				i++
				continue
			}
			return i + 1
		}
	}
	return len(query)
}

// dollarQuoteEnd finds the end of a PostgreSQL dollar quoted string such as $body$...$body$.
func dollarQuoteEnd(query string, start int) (int, bool) {
	tagEnd := strings.IndexByte(query[start+1:], '$')
	if tagEnd < 0 {
		return 0, false
	}
	tag := query[start : start+tagEnd+2]
	for _, r := range tag[1 : len(tag)-1] {
		if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return 0, false
		}
	}
	if len(tag) > 2 && tag[1] >= '0' && tag[1] <= '9' {
		return 0, false
	}
	end := strings.Index(query[start+len(tag):], tag)
	if end < 0 {
		return len(query), true
	}
	return start + len(tag) + end + len(tag), true
}

// splitStatements splits a script at the semicolons outside quoted text, comments and dollar
// quoted strings, and drops what holds no SQL.
func splitStatements(d Dialect, script string) []string {
	var statements []string
	start, hasSQL := 0, false
	flush := func(end int) {
		if statement := strings.TrimSpace(script[start:end]); statement != "" && hasSQL {
			statements = append(statements, statement)
		}
		start, hasSQL = end+1, false
	}
	for i := 0; i < len(script); i++ {
		c := script[i]
		switch {
		case c == '\'' || c == '"':
			i = closingQuote(script, i) - 1
			hasSQL = true
		case c == '-' && i+1 < len(script) && script[i+1] == '-':
			end := strings.IndexByte(script[i:], '\n')
			if end < 0 {
				end = len(script) - i
			}
			i += end - 1
		case c == '/' && i+1 < len(script) && script[i+1] == '*':
			end := strings.Index(script[i+2:], "*/")
			if end < 0 {
				i = len(script)
				continue
			}
			i += end + 3
		case c == '$' && d == Postgres:
			if end, ok := dollarQuoteEnd(script, i); ok {
				i = end - 1
			}
			hasSQL = true
		case c == ';':
			flush(i)
		case c != ' ' && c != '\t' && c != '\n' && c != '\r':
			hasSQL = true
		}
	}
	flush(len(script))
	return statements
}
