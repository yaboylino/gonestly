package scaffold

import (
	"fmt"
	"strings"
	"unicode"
)

// Names are the spellings of one feature name, such as "order-items", in generated code.
type Names struct {
	// Package is the Go package and directory: orderitems.
	Package string
	// Kebab is used in URLs and module names: order-items.
	Kebab string
	// Snake is used for tables: order_items.
	Snake string
	// Model is the singular type name: OrderItem.
	Model string
	// Words is the plural in words, for comments: order items.
	Words string
	// ModelWords is the singular in words: order item.
	ModelWords string
}

// NewNames splits a name in any spelling, such as OrderItems, order_items or order-items.
func NewNames(name string) (Names, error) {
	words := splitWords(name)
	if len(words) == 0 {
		return Names{}, fmt.Errorf("%q is not a valid name; use letters, digits, dashes or underscores", name)
	}
	if unicode.IsDigit(rune(words[0][0])) {
		return Names{}, fmt.Errorf("%q must start with a letter", name)
	}
	singular := append(append([]string{}, words[:len(words)-1]...), singularize(words[len(words)-1]))
	return Names{
		Package:    strings.Join(words, ""),
		Kebab:      strings.Join(words, "-"),
		Snake:      strings.Join(words, "_"),
		Model:      pascal(singular),
		Words:      strings.Join(words, " "),
		ModelWords: strings.Join(singular, " "),
	}, nil
}

func splitWords(name string) []string {
	var words []string
	var current []rune
	runes := []rune(name)
	flush := func() {
		if len(current) > 0 {
			words = append(words, strings.ToLower(string(current)))
			current = nil
		}
	}
	for i, r := range runes {
		switch {
		case r == '-' || r == '_' || r == ' ' || r == '.':
			flush()
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			return nil
		case unicode.IsUpper(r) && i > 0 && (unicode.IsLower(runes[i-1]) || (i+1 < len(runes) && unicode.IsLower(runes[i+1]) && unicode.IsUpper(runes[i-1]))):
			flush()
			current = append(current, r)
		default:
			current = append(current, r)
		}
	}
	flush()
	return words
}

// singularize handles regular English plurals: products, categories, boxes.
func singularize(word string) string {
	switch {
	case strings.HasSuffix(word, "ies") && len(word) > 3:
		return word[:len(word)-3] + "y"
	case strings.HasSuffix(word, "sses"), strings.HasSuffix(word, "xes"), strings.HasSuffix(word, "ches"), strings.HasSuffix(word, "shes"), strings.HasSuffix(word, "zes"):
		return word[:len(word)-2]
	case strings.HasSuffix(word, "ss"), strings.HasSuffix(word, "us"):
		return word
	case strings.HasSuffix(word, "s") && len(word) > 1:
		return word[:len(word)-1]
	default:
		return word
	}
}

func pascal(words []string) string {
	var b strings.Builder
	for _, w := range words {
		switch w {
		case "id", "url", "api", "http", "json", "sql", "uuid":
			b.WriteString(strings.ToUpper(w))
		default:
			r := []rune(w)
			r[0] = unicode.ToUpper(r[0])
			b.WriteString(string(r))
		}
	}
	return b.String()
}
