// Package prompt asks the user questions on a terminal.
package prompt

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// ErrNoInput reports that the input ended before an answer was given.
var ErrNoInput = errors.New("no answer was given and the input has ended")

// Prompter asks questions on out and reads the answers from in.
type Prompter struct {
	in  *bufio.Reader
	out io.Writer
	// secret reads an answer without showing it; it defaults to reading the terminal.
	secret func() (string, error)
}

// New returns a Prompter for the given input and output. When in is a terminal, a password is
// typed without being shown.
func New(in io.Reader, out io.Writer) *Prompter {
	p := &Prompter{in: bufio.NewReader(in), out: out}
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		p.secret = func() (string, error) {
			b, err := term.ReadPassword(int(f.Fd()))
			_, _ = fmt.Fprintln(out)
			return string(b), err
		}
	}
	return p
}

// Interactive reports whether questions can be asked: the input is a terminal.
func Interactive(in io.Reader) bool {
	f, ok := in.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func (p *Prompter) line() (string, error) {
	text, err := p.in.ReadString('\n')
	if err != nil && (text == "" || !errors.Is(err, io.EOF)) {
		if errors.Is(err, io.EOF) {
			return "", ErrNoInput
		}
		return "", err
	}
	return strings.TrimRight(text, "\r\n"), nil
}

// Ask asks for a line of text, and returns def when the answer is empty.
func (p *Prompter) Ask(label, def string) (string, error) {
	if def != "" {
		_, _ = fmt.Fprintf(p.out, "%s (%s): ", label, def)
	} else {
		_, _ = fmt.Fprintf(p.out, "%s: ", label)
	}
	answer, err := p.line()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(answer) == "" {
		return def, nil
	}
	return strings.TrimSpace(answer), nil
}

// AskInt asks for a whole number, and asks again until it is one.
func (p *Prompter) AskInt(label string, def int) (int, error) {
	for {
		answer, err := p.Ask(label, strconv.Itoa(def))
		if err != nil {
			return 0, err
		}
		if n, err := strconv.Atoi(answer); err == nil {
			return n, nil
		}
		_, _ = fmt.Fprintf(p.out, "%q is not a whole number\n", answer)
	}
}

// Password asks for a secret, which is not shown while it is typed.
func (p *Prompter) Password(label string) (string, error) {
	_, _ = fmt.Fprintf(p.out, "%s: ", label)
	if p.secret != nil {
		return p.secret()
	}
	return p.line()
}

// Choose asks the user to pick one of the options, by number or by name, and asks again until
// the answer is one of them.
func (p *Prompter) Choose(label string, options []string, def string) (string, error) {
	_, _ = fmt.Fprintln(p.out, label)
	for i, option := range options {
		_, _ = fmt.Fprintf(p.out, "  %d) %s\n", i+1, option)
	}
	for {
		answer, err := p.Ask("Choose", def)
		if err != nil {
			return "", err
		}
		if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(options) {
			return options[n-1], nil
		}
		for _, option := range options {
			if strings.EqualFold(answer, option) {
				return option, nil
			}
		}
		_, _ = fmt.Fprintf(p.out, "%q is not one of the options\n", answer)
	}
}

// Confirm asks a yes or no question; an empty answer is def.
func (p *Prompter) Confirm(label string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		answer, err := p.Ask(label+" ("+hint+")", "")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "":
			return def, nil
		case "y", "yes", "j", "ja":
			return true, nil
		case "n", "no", "nee":
			return false, nil
		}
		_, _ = fmt.Fprintln(p.out, "Answer y or n.")
	}
}
