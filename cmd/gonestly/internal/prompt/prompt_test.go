package prompt

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func newTest(input string) (*Prompter, *bytes.Buffer) {
	var out bytes.Buffer
	return New(strings.NewReader(input), &out), &out
}

func TestAsk(t *testing.T) {
	p, out := newTest("  db.example.com \n\n")
	if got, err := p.Ask("Host", "localhost"); err != nil || got != "db.example.com" {
		t.Errorf("Ask = %q, %v", got, err)
	}
	if got, err := p.Ask("User", "root"); err != nil || got != "root" {
		t.Errorf("Ask with the default = %q, %v", got, err)
	}
	if !strings.Contains(out.String(), "Host (localhost): ") {
		t.Errorf("output = %q", out)
	}
	if _, err := p.Ask("Port", ""); !errors.Is(err, ErrNoInput) {
		t.Errorf("Ask at the end of the input = %v", err)
	}
}

func TestAnswerWithoutNewline(t *testing.T) {
	p, _ := newTest("last")
	if got, err := p.Ask("Name", ""); err != nil || got != "last" {
		t.Errorf("Ask = %q, %v", got, err)
	}
}

func TestAskInt(t *testing.T) {
	p, _ := newTest("abc\n\n5433\n")
	if got, err := p.AskInt("Port", 5432); err != nil || got != 5432 {
		t.Errorf("AskInt with the default = %d, %v", got, err)
	}
	if got, err := p.AskInt("Port", 5432); err != nil || got != 5433 {
		t.Errorf("AskInt = %d, %v", got, err)
	}
	p, out := newTest("abc\n7\n")
	if got, err := p.AskInt("Port", 1); err != nil || got != 7 || !strings.Contains(out.String(), `"abc" is not a whole number`) {
		t.Errorf("AskInt after a mistake = %d, %v, %q", got, err, out)
	}
}

func TestChoose(t *testing.T) {
	options := []string{"sqlite", "mysql", "postgres"}
	for input, want := range map[string]string{"2\n": "mysql", "POSTGRES\n": "postgres", "\n": "sqlite", "9\nfoo\n3\n": "postgres"} {
		p, _ := newTest(input)
		if got, err := p.Choose("Which database?", options, "sqlite"); err != nil || got != want {
			t.Errorf("Choose(%q) = %q, %v, want %q", input, got, err, want)
		}
	}
	p, out := newTest("")
	if _, err := p.Choose("Which database?", options, ""); !errors.Is(err, ErrNoInput) || !strings.Contains(out.String(), "  2) mysql") {
		t.Errorf("Choose without input = %v, %q", err, out)
	}
}

func TestPassword(t *testing.T) {
	p, out := newTest("s3cret\n")
	if got, err := p.Password("Password"); err != nil || got != "s3cret" || out.String() != "Password: " {
		t.Errorf("Password = %q, %v, %q", got, err, out)
	}
	p.secret = func() (string, error) { return "typed", nil }
	if got, _ := p.Password("Password"); got != "typed" {
		t.Errorf("Password with a secret reader = %q", got)
	}
}
