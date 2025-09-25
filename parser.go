package lockgaurd

import (
	"strings"
	"unicode"
)

type valueParser struct {
	s   string
	pos int
}

func parseDirective(text string) (string, bool) {
	// Parse directive, which looks like: "//lockguard:protected_by s1.mu". We want to return "s1.mu"

	p := valueParser{
		s:   strings.TrimSpace(text),
		pos: 0,
	}

	// TODO we'll need to change this when we add new directives.
	if !p.consume("//lockguard:protected_by") {
		return "", false
	}
	p.skipWhiteSpace()
	if p.done() {
		return "", false
	}
	return p.s[p.pos:], true
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
