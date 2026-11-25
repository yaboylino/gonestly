package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"strings"
)

// readFiles reads the dotenv files in order, later files overriding earlier ones.
func readFiles(paths []string) (map[string]string, error) {
	vars := map[string]string{}
	for _, path := range paths {
		f, err := os.Open(path) //nolint:gosec // the paths come from the application's own options
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
		fileVars, err := parseDotenv(f, path)
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		maps.Copy(vars, fileVars)
	}
	return vars, nil
}

// parseDotenv parses KEY=VALUE lines. Values may be double quoted with escapes, single quoted
// literally, or unquoted with an optional trailing comment. Variables are not expanded.
func parseDotenv(r io.Reader, name string) (map[string]string, error) {
	vars := map[string]string{}
	scanner := bufio.NewScanner(r)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))

		key, rawValue, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !validKey(key) {
			return nil, fmt.Errorf("config: %s:%d: expected KEY=VALUE", name, lineNo)
		}
		value, err := parseValue(strings.TrimSpace(rawValue))
		if err != nil {
			return nil, fmt.Errorf("config: %s:%d: %w", name, lineNo, err)
		}
		vars[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("config: %s: %w", name, err)
	}
	return vars, nil
}

func parseValue(raw string) (string, error) {
	switch {
	case strings.HasPrefix(raw, `"`):
		return parseDoubleQuoted(raw[1:])
	case strings.HasPrefix(raw, "'"):
		end := strings.IndexByte(raw[1:], '\'')
		if end < 0 {
			return "", errors.New("unterminated single quoted value")
		}
		return raw[1 : end+1], checkTrailing(raw[end+2:])
	default:
		if i := strings.Index(raw, " #"); i >= 0 {
			raw = raw[:i]
		}
		return strings.TrimSpace(raw), nil
	}
}

func parseDoubleQuoted(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			return b.String(), checkTrailing(s[i+1:])
		case c == '\\' && i+1 < len(s):
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(s[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", errors.New("unterminated double quoted value")
}

func checkTrailing(rest string) error {
	rest = strings.TrimSpace(rest)
	if rest != "" && !strings.HasPrefix(rest, "#") {
		return fmt.Errorf("unexpected %q after the closing quote", rest)
	}
	return nil
}

func validKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		isLetter := r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
		isDigitOrDot := (r >= '0' && r <= '9') || r == '.'
		if !isLetter && (i == 0 || !isDigitOrDot) {
			return false
		}
	}
	return true
}
