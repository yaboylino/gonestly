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
		if end, _ := skipLexeme(d, query, i); end > i {
			b.WriteString(query[i:end])
			i = end - 1
			continue
		}
		switch {
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

// skipLexeme returns the end of the quoted text or comment that starts at i, and whether it
// is a comment. The end is i when neither starts there.
func skipLexeme(d Dialect, s string, i int) (end int, comment bool) {
	c := s[i]
	next := byte(0)
	if i+1 < len(s) {
		next = s[i+1]
	}
	switch {
	case c == '\'' || c == '"' || (c == '`' && d == MySQL):
		return closingQuote(d, s, i), false
	case c == '$' && d == Postgres:
		if end, ok := dollarQuoteEnd(s, i); ok {
			return end, false
		}
	case c == '/' && next == '*':
		if end := strings.Index(s[i+2:], "*/"); end >= 0 {
			return i + end + 4, true
		}
		return len(s), true
	case c == '#' && d == MySQL, c == '-' && next == '-' && (d != MySQL || lineCommentStart(s, i)):
		if end := strings.IndexByte(s[i:], '\n'); end >= 0 {
			return i + end, true
		}
		return len(s), true
	}
	return i, false
}

// lineCommentStart reports whether the -- at i starts a comment in MySQL, which needs white
// space after it; otherwise 5--3 would lose its second operand.
func lineCommentStart(s string, i int) bool {
	return i+2 >= len(s) || s[i+2] <= ' '
}

// closingQuote returns the index after the quote that closes the one at start.
func closingQuote(d Dialect, query string, start int) int {
	quote := query[start]
	for i := start + 1; i < len(query); i++ {
		switch {
		case query[i] == '\\' && d == MySQL && quote != '`':
			i++
		case query[i] == quote:
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

// splitStatements splits a script at the semicolons outside quoted text, comments and the
// bodies of routines and triggers, and drops what holds no SQL.
func splitStatements(d Dialect, script string) []string {
	var statements []string
	start, hasSQL := 0, false
	var blocks blockTracker
	flush := func(end int) {
		if statement := strings.TrimSpace(script[start:end]); statement != "" && hasSQL {
			statements = append(statements, statement)
		}
		start, hasSQL, blocks = end+1, false, blockTracker{}
	}
	for i := 0; i < len(script); i++ {
		c := script[i]
		if end, comment := skipLexeme(d, script, i); end > i {
			// A comment such as /*!40101 ... */ or /*+ ... */ is an instruction to the database.
			if !comment || strings.HasPrefix(script[i:end], "/*!") || strings.HasPrefix(script[i:end], "/*+") {
				hasSQL = true
				blocks.previous = ""
			}
			i = end - 1
			continue
		}
		switch {
		case isWordByte(c) && (c < '0' || c > '9'):
			end := i + 1
			for end < len(script) && isWordByte(script[end]) {
				end++
			}
			blocks.word(d, strings.ToUpper(script[i:end]))
			hasSQL = true
			i = end - 1
		case c == ';' && blocks.depth == 0:
			flush(i)
		case c != ' ' && c != '\t' && c != '\n' && c != '\r':
			hasSQL = true
			blocks.previous = ""
		}
	}
	flush(len(script))
	return statements
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

// blockTracker follows the BEGIN ... END bodies of one statement, whose semicolons do not end
// it: BEGIN ATOMIC in PostgreSQL, and the routines, triggers and events of MySQL.
type blockTracker struct {
	depth    int
	first    string
	previous string
	routine  bool
}

func (t *blockTracker) word(d Dialect, word string) {
	previous := t.previous
	t.previous = word
	if t.first == "" {
		t.first = word
	}
	switch d {
	case Postgres:
		switch {
		case t.depth == 0 && word == "ATOMIC" && previous == "BEGIN":
			t.depth = 1
		case t.depth > 0 && word == "CASE":
			t.depth++
		case t.depth > 0 && word == "END":
			t.depth--
		}
	case MySQL:
		switch {
		case previous == "END" && (word == "IF" || word == "LOOP" || word == "WHILE" || word == "REPEAT"):
			// END IF and its kind close a construct that was never counted as a block.
			t.depth++
		case previous == "END" && word == "CASE":
		case t.depth == 0 && (t.first == "CREATE" || t.first == "ALTER"):
			switch word {
			case "PROCEDURE", "FUNCTION", "TRIGGER", "EVENT":
				t.routine = true
			case "BEGIN":
				if t.routine {
					t.depth = 1
				}
			}
		case t.depth > 0 && (word == "BEGIN" || word == "CASE"):
			t.depth++
		case t.depth > 0 && word == "END":
			t.depth--
		}
	}
}
