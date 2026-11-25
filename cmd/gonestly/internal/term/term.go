// Package term writes the CLI's output, with colors when the output is a terminal.
package term

import (
	"fmt"
	"io"
	"os"
)

// Printer writes status lines in a consistent style.
type Printer struct {
	out   io.Writer
	color bool
}

// New returns a printer that uses colors when out is a terminal and NO_COLOR is not set.
func New(out io.Writer) *Printer {
	color := false
	if f, ok := out.(*os.File); ok && os.Getenv("NO_COLOR") == "" {
		if info, err := f.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			color = true
		}
	}
	return &Printer{out: out, color: color}
}

func (p *Printer) paint(code, text string) string {
	if !p.color {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

// Success prints a line that reports something that worked.
func (p *Printer) Success(format string, args ...any) {
	fmt.Fprintf(p.out, "%s %s\n", p.paint("32", "✓"), fmt.Sprintf(format, args...))
}

// Failure prints a line that reports something that failed.
func (p *Printer) Failure(format string, args ...any) {
	fmt.Fprintf(p.out, "%s %s\n", p.paint("31", "✗"), fmt.Sprintf(format, args...))
}

// Info prints a neutral status line.
func (p *Printer) Info(format string, args ...any) {
	fmt.Fprintf(p.out, "%s %s\n", p.paint("36", "•"), fmt.Sprintf(format, args...))
}

// Detail prints an indented line under a status line, dimmed.
func (p *Printer) Detail(format string, args ...any) {
	fmt.Fprintf(p.out, "  %s\n", p.paint("2", fmt.Sprintf(format, args...)))
}

// Created reports a file that was written.
func (p *Printer) Created(path string) {
	fmt.Fprintf(p.out, "  %s %s\n", p.paint("32", "create"), path)
}

// Updated reports a file that was changed.
func (p *Printer) Updated(path string) {
	fmt.Fprintf(p.out, "  %s %s\n", p.paint("33", "update"), path)
}

// Plain prints text as it is.
func (p *Printer) Plain(format string, args ...any) {
	fmt.Fprintf(p.out, format, args...)
}
