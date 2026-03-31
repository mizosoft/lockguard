package lockguard

import (
	"strings"
	"unicode"
)

type valueParser struct {
	s   string
	pos int
}

func parseCommentDirective(text string) (protectionDirective, string, bool) {
	// What we're looking for is something like: "//lockguard:protected_by s1.mu".
	p := valueParser{
		s:   strings.TrimSpace(text),
		pos: 0,
	}

	if !p.consume("//lockguard:") {
		return "", "", false
	}

	for _, directive := range protectionDirectives {
		if p.consume(string(directive)) {
			p.skipWhiteSpace()
			if p.done() {
				return "", "", false
			}
			return directive, p.s[p.pos:], true
		}
	}
	return "", "", false
}

func (p *valueParser) consume(t string) bool {
	if p.pos > len(p.s)-len(t) {
		return false
	}
	for i := 0; i < len(t); i++ {
		if p.s[p.pos+i] != t[i] {
			return false
		}
	}
	p.pos += len(t)
	return true
}

func (p *valueParser) skipWhiteSpace() {
	for !p.done() && unicode.IsSpace(rune(p.s[p.pos])) {
		p.pos++
	}
}

func (p *valueParser) done() bool {
	return p.pos == len(p.s)
}

func (p *valueParser) remaining() string {
	return p.s[p.pos:]
}
