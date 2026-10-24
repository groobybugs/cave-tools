// Package jsjson reproduces the SyntaxError message that V8's JSON.parse
// throws for malformed input.
//
// The command line ports call JSON.parse and surface the error message
// verbatim, so a Go implementation that reports encoding/json's wording
// diverges. SyntaxErrorMessage walks the input the way V8's JsonParser does
// (src/json/json-parser.cc) and formats the same message templates.
//
// Positions are UTF-16 code units, like JavaScript string indices, and the
// line/column calculation follows V8's CalculateFileLocation (only \r and \n
// terminate a line, \r\n counts once).
package jsjson

import (
	"fmt"
	"unicode/utf16"
	"unicode/utf8"
)

type token uint8

const (
	tokEOS token = iota
	tokNumber
	tokString
	tokTrue
	tokFalse
	tokNull
	tokLBrace
	tokRBrace
	tokLBrack
	tokRBrack
	tokColon
	tokComma
	tokWhitespace
	tokIllegal
)

func tokenForUnit(c uint16) token {
	switch c {
	case '"':
		return tokString
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '-':
		return tokNumber
	case 't':
		return tokTrue
	case 'f':
		return tokFalse
	case 'n':
		return tokNull
	case '{':
		return tokLBrace
	case '}':
		return tokRBrace
	case '[':
		return tokLBrack
	case ']':
		return tokRBrack
	case ':':
		return tokColon
	case ',':
		return tokComma
	case ' ', '\t', '\r', '\n':
		return tokWhitespace
	default:
		return tokIllegal
	}
}

// Message templates from src/common/message-template.h. Only the fixed part is
// stored here; arguments are added by syntaxError.
const (
	tmplUnexpectedNumber       = "Unexpected number in JSON at position %d (line %d column %d)"
	tmplUnexpectedString       = "Unexpected string in JSON at position %d (line %d column %d)"
	tmplUnterminatedString     = "Unterminated string in JSON at position %d (line %d column %d)"
	tmplPropNameOrRBrace       = "Expected property name or '}' in JSON at position %d (line %d column %d)"
	tmplCommaOrRBrack          = "Expected ',' or ']' after array element in JSON at position %d (line %d column %d)"
	tmplCommaOrRBrace          = "Expected ',' or '}' after property value in JSON at position %d (line %d column %d)"
	tmplDoubleQuotedPropName   = "Expected double-quoted property name in JSON at position %d (line %d column %d)"
	tmplExponentMissing        = "Exponent part is missing a number in JSON at position %d (line %d column %d)"
	tmplColonAfterPropName     = "Expected ':' after property name in JSON at position %d (line %d column %d)"
	tmplUnterminatedFractional = "Unterminated fractional number in JSON at position %d (line %d column %d)"
	tmplNonWhitespace          = "Unexpected non-whitespace character after JSON at position %d (line %d column %d)"
	tmplBadEscapedCharacter    = "Bad escaped character in JSON at position %d (line %d column %d)"
	tmplBadControlCharacter    = "Bad control character in string literal in JSON at position %d (line %d column %d)"
	tmplBadUnicodeEscape       = "Bad Unicode escape in JSON at position %d (line %d column %d)"
	tmplNoNumberAfterMinus     = "No number after minus sign in JSON at position %d (line %d column %d)"
)

const (
	// kMaxContextCharacters and kMinOriginalSourceLengthForContext from
	// json-parser.h.
	maxContextCharacters         = 10
	minSourceLengthForContext    = maxContextCharacters*2 + 1
	tmplUnexpectedEndOfJSONInput = "Unexpected end of JSON input"
)

type syntaxError struct {
	message string
}

// SyntaxErrorMessage returns the message V8 attaches to the SyntaxError thrown
// by JSON.parse(input), or "" when input is valid JSON.
func SyntaxErrorMessage(input string) string {
	p := &parser{src: toUTF16(input)}
	if err := p.parse(); err != nil {
		return err.message
	}
	return ""
}

func toUTF16(s string) []uint16 {
	units := make([]uint16, 0, len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			units = append(units, 0xFFFD)
			i++
			continue
		}
		if r > 0xFFFF {
			r -= 0x10000
			units = append(units, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
		} else {
			units = append(units, uint16(r))
		}
		i += size
	}
	return units
}

func utf16String(units []uint16) string {
	return string(utf16.Decode(units))
}

type parser struct {
	src  []uint16
	pos  int
	next token
}

func (p *parser) current() int {
	if p.pos < len(p.src) {
		return int(p.src[p.pos])
	}
	return -1
}

func (p *parser) nextCharacter() int {
	p.pos++
	return p.current()
}

func (p *parser) skipWhitespace() {
	p.next = tokEOS
	for p.pos < len(p.src) {
		t := tokenForUnit(p.src[p.pos])
		if t != tokWhitespace {
			p.next = t
			return
		}
		p.pos++
	}
}

func (p *parser) advance() { p.pos++ }

func (p *parser) check(t token) bool {
	p.skipWhitespace()
	if p.next != t {
		return false
	}
	p.advance()
	return true
}

func (p *parser) expect(t token, tmpl string) *syntaxError {
	if p.next == t {
		p.advance()
		return nil
	}
	return p.report(t, tmpl, p.pos)
}

func (p *parser) expectNext(t token, tmpl string) *syntaxError {
	p.skipWhitespace()
	return p.expect(t, tmpl)
}

func (p *parser) parse() *syntaxError {
	if err := p.parseValue(); err != nil {
		return err
	}
	if !p.check(tokEOS) {
		return p.report(p.next, tmplNonWhitespace, p.pos)
	}
	return nil
}

func (p *parser) parseValue() *syntaxError {
	p.skipWhitespace()
	switch p.next {
	case tokNumber:
		return p.parseNumber()
	case tokString:
		p.advance()
		return p.scanString()
	case tokTrue:
		return p.scanLiteral("true")
	case tokFalse:
		return p.scanLiteral("false")
	case tokNull:
		return p.scanLiteral("null")
	case tokLBrace:
		return p.parseObject()
	case tokLBrack:
		return p.parseArray()
	default:
		return p.reportUnexpectedCharacter(p.current())
	}
}

func (p *parser) parseObject() *syntaxError {
	p.advance() // consume '{'
	if p.check(tokRBrace) {
		return nil
	}
	first := true
	for {
		tmpl := tmplDoubleQuotedPropName
		if first {
			tmpl = tmplPropNameOrRBrace
		}
		if err := p.expectNext(tokString, tmpl); err != nil {
			return err
		}
		if err := p.scanString(); err != nil {
			return err
		}
		if err := p.expectNext(tokColon, tmplColonAfterPropName); err != nil {
			return err
		}
		if err := p.parseValue(); err != nil {
			return err
		}
		first = false
		if !p.check(tokComma) {
			break
		}
	}
	return p.expect(tokRBrace, tmplCommaOrRBrace)
}

func (p *parser) parseArray() *syntaxError {
	p.advance() // consume '['
	if p.check(tokRBrack) {
		return nil
	}
	// V8 has a number-only fast path: while the next token is a number it
	// consumes elements without going through ParseJsonValueRecursive. Error
	// positions and messages are the same, so mirror the control flow.
	p.skipWhitespace()
	if p.next == tokNumber {
		for p.next == tokNumber {
			if err := p.parseNumber(); err != nil {
				return err
			}
			if p.check(tokComma) {
				p.skipWhitespace()
				continue
			}
			return p.expect(tokRBrack, tmplCommaOrRBrack)
		}
	}
	if err := p.parseValue(); err != nil {
		return err
	}
	for p.check(tokComma) {
		if err := p.parseValue(); err != nil {
			return err
		}
	}
	return p.expect(tokRBrack, tmplCommaOrRBrack)
}

func isDecimalDigit(c int) bool { return c >= '0' && c <= '9' }

func isNumberPart(c uint16) bool {
	switch c {
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '.', 'e', 'E', '+', '-':
		return true
	}
	return false
}

func (p *parser) advanceToNonDecimal() {
	for p.pos < len(p.src) && isDecimalDigit(int(p.src[p.pos])) {
		p.pos++
	}
}

func (p *parser) parseNumber() *syntaxError {
	sign := 1
	c := p.current()
	if c == '-' {
		sign = -1
		c = p.nextCharacter()
	}
	if c == '0' {
		// A prefixed zero is only allowed when it is the only digit before a
		// decimal point or exponent.
		c = p.nextCharacter()
		if c >= 0 && c <= 0xFF && isNumberPart(uint16(c)) {
			if isDecimalDigit(c) {
				return p.report(tokNumber, "", p.pos)
			}
		} else if sign > 0 {
			return nil
		}
	} else {
		start := p.pos
		limit := p.pos + 9
		if limit > len(p.src) {
			limit = len(p.src)
		}
		for p.pos < limit && isDecimalDigit(int(p.src[p.pos])) {
			p.pos++
		}
		if p.pos == start {
			return p.report(tokIllegal, tmplNoNumberAfterMinus, p.pos)
		}
		c = p.current()
		if !(c >= 0 && c <= 0xFF && isNumberPart(uint16(c))) {
			return nil
		}
		p.advanceToNonDecimal()
	}

	if p.current() == '.' {
		c = p.nextCharacter()
		if !isDecimalDigit(c) {
			return p.report(tokIllegal, tmplUnterminatedFractional, p.pos)
		}
		p.advanceToNonDecimal()
	}

	if c := p.current(); c == 'e' || c == 'E' {
		c = p.nextCharacter()
		if c == '-' || c == '+' {
			c = p.nextCharacter()
		}
		if !isDecimalDigit(c) {
			return p.report(tokIllegal, tmplExponentMissing, p.pos)
		}
		p.advanceToNonDecimal()
	}
	return nil
}

func (p *parser) scanLiteral(lit string) *syntaxError {
	n := len(lit)
	remaining := len(p.src) - p.pos
	matches := remaining >= n
	for i := 1; matches && i < n; i++ {
		if p.src[p.pos+i] != uint16(lit[i]) {
			matches = false
		}
	}
	if matches {
		p.pos += n
		return nil
	}
	p.pos++
	limit := n - 1
	if remaining-1 < limit {
		limit = remaining - 1
	}
	for i := 0; i < limit; i++ {
		if p.src[p.pos] != uint16(lit[1+i]) {
			return p.reportUnexpectedCharacter(p.current())
		}
		p.pos++
	}
	return p.report(tokEOS, "", p.pos)
}

func mayTerminateString(c uint16) bool {
	return c == '"' || c == '\\' || c < 0x20
}

type escapeKind uint8

const (
	escIllegal escapeKind = iota
	escSelf
	escBackspace
	escTab
	escNewLine
	escFormFeed
	escCarriageReturn
	escUnicode
)

func escapeKindFor(c uint16) escapeKind {
	switch c {
	case '"', '\\', '/':
		return escSelf
	case 'b':
		return escBackspace
	case 't':
		return escTab
	case 'n':
		return escNewLine
	case 'f':
		return escFormFeed
	case 'r':
		return escCarriageReturn
	case 'u':
		return escUnicode
	}
	return escIllegal
}

func (p *parser) scanUnicodeCharacter() int {
	value := 0
	for i := 0; i < 4; i++ {
		digit := hexValue(p.nextCharacter())
		if digit < 0 {
			return -1
		}
		value = value*16 + digit
	}
	return value
}

func hexValue(c int) int {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return -1
}

func (p *parser) scanString() *syntaxError {
	for {
		for p.pos < len(p.src) {
			c := p.src[p.pos]
			if c <= 0xFF && mayTerminateString(c) {
				break
			}
			p.pos++
		}
		if p.pos >= len(p.src) {
			return p.report(tokIllegal, tmplUnterminatedString, p.pos)
		}
		switch p.src[p.pos] {
		case '"':
			p.advance()
			return nil
		case '\\':
			c := p.nextCharacter()
			if c < 0 || c > 0xFF {
				return p.reportUnexpectedCharacter(c)
			}
			switch escapeKindFor(uint16(c)) {
			case escSelf, escBackspace, escTab, escNewLine, escFormFeed, escCarriageReturn:
				// Nothing to record; the character is consumed below.
			case escUnicode:
				if p.scanUnicodeCharacter() < 0 {
					return p.report(tokIllegal, tmplBadUnicodeEscape, p.pos)
				}
			default:
				return p.report(tokIllegal, tmplBadEscapedCharacter, p.pos)
			}
			p.advance()
		default:
			return p.report(tokIllegal, tmplBadControlCharacter, p.pos)
		}
	}
}

func (p *parser) reportUnexpectedCharacter(c int) *syntaxError {
	t := tokIllegal
	switch {
	case c < 0:
		t = tokEOS
	case c <= 0xFF:
		t = tokenForUnit(uint16(c))
	}
	return p.report(t, "", p.pos)
}

func (p *parser) report(t token, tmpl string, pos int) *syntaxError {
	if tmpl != "" {
		line, column := p.lineColumn(pos)
		return &syntaxError{message: fmt.Sprintf(tmpl, pos, line, column)}
	}
	switch t {
	case tokEOS:
		return &syntaxError{message: tmplUnexpectedEndOfJSONInput}
	case tokNumber, tokString:
		message := tmplUnexpectedNumber
		if t == tokString {
			message = tmplUnexpectedString
		}
		line, column := p.lineColumn(pos)
		return &syntaxError{message: fmt.Sprintf(message, pos, line, column)}
	default:
		return &syntaxError{message: p.unexpectedTokenMessage(pos)}
	}
}

// unexpectedTokenMessage mirrors LookUpErrorMessageForJsonToken and
// GetErrorMessageWithEllipses.
func (p *parser) unexpectedTokenMessage(pos int) string {
	if isSpecialString(p.src) {
		return `"` + utf16String(p.src) + `" is not valid JSON`
	}
	unit := ""
	if pos < len(p.src) {
		unit = utf16String(p.src[pos : pos+1])
	}
	length := len(p.src)
	if length >= minSourceLengthForContext {
		switch {
		case pos < maxContextCharacters:
			return fmt.Sprintf("Unexpected token '%s', \"%s\"... is not valid JSON",
				unit, utf16String(p.src[:pos+maxContextCharacters]))
		case pos < length-maxContextCharacters:
			return fmt.Sprintf("Unexpected token '%s', ...\"%s\"... is not valid JSON",
				unit, utf16String(p.src[pos-maxContextCharacters:pos+maxContextCharacters]))
		default:
			return fmt.Sprintf("Unexpected token '%s', ...\"%s\" is not valid JSON",
				unit, utf16String(p.src[pos-maxContextCharacters:length]))
		}
	}
	return fmt.Sprintf("Unexpected token '%s', \"%s\" is not valid JSON", unit, utf16String(p.src))
}

func isSpecialString(units []uint16) bool {
	switch utf16String(units) {
	case "[object Object]", "undefined", "Infinity", "NaN":
		return true
	}
	return false
}

// lineColumn mirrors CalculateFileLocation: 1-based, \r\n is one terminator,
// and the column counts UTF-16 code units since the last terminator.
func (p *parser) lineColumn(pos int) (line, column int) {
	line = 1
	lastLineBreak := 0
	i := 0
	for i < pos {
		c := uint16(0)
		if i < len(p.src) {
			c = p.src[i]
		}
		if c == '\r' && i < pos-1 {
			next := uint16(0)
			if i+1 < len(p.src) {
				next = p.src[i+1]
			}
			if next == '\n' {
				i++
			}
		}
		if c == '\r' || c == '\n' {
			line++
			lastLineBreak = i + 1
		}
		i++
	}
	return line, 1 + pos - lastLineBreak
}
