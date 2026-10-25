// Copyright 2025 The go-taas Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

// A minimal JSON reader.
//
// `encoding/json` pulls in reflection, which costs hundreds of kilobytes in a
// Wasm filter that runs on every inference request. The plugin only needs
// three things — read a string or number at a known-ish path, find the
// `usage` object, and write a small object — so this file does exactly that
// and nothing more.
//
// It is deliberately strict about what it accepts: malformed input returns
// "not found" rather than a guess, because every value it produces feeds
// either an authorization decision or a billing record.

import "strings"

// jsonValue is a parsed JSON node. Only the shapes the plugin needs are
// represented; anything else parses to jsonNull.
type jsonValue struct {
	kind   jsonKind
	str    string
	num    float64
	object map[string]*jsonValue
	array  []*jsonValue
}

type jsonKind int

const (
	jsonNull jsonKind = iota
	jsonString
	jsonNumber
	jsonBool
	jsonObject
	jsonArray
)

// get returns a member of an object, or nil.
func (v *jsonValue) get(key string) *jsonValue {
	if v == nil || v.kind != jsonObject {
		return nil
	}
	return v.object[key]
}

// stringOr returns a string member, or "".
func (v *jsonValue) stringOr(key string) string {
	m := v.get(key)
	if m == nil || m.kind != jsonString {
		return ""
	}
	return m.str
}

// intOr returns an integer member, or 0.
func (v *jsonValue) intOr(key string) int64 {
	m := v.get(key)
	if m == nil || m.kind != jsonNumber {
		return 0
	}
	return int64(m.num)
}

// boolOr returns a boolean member, or false.
func (v *jsonValue) boolOr(key string) bool {
	m := v.get(key)
	return m != nil && m.kind == jsonBool && m.str == "true"
}

// parseJSON parses s. ok is false when the input is not a single JSON value.
func parseJSON(s string) (*jsonValue, bool) {
	p := &jsonParser{src: s}
	v, ok := p.parseValue()
	if !ok {
		return nil, false
	}
	p.skipSpace()
	if p.pos != len(p.src) {
		return nil, false
	}
	return v, true
}

type jsonParser struct {
	src string
	pos int
}

func (p *jsonParser) skipSpace() {
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *jsonParser) parseValue() (*jsonValue, bool) {
	p.skipSpace()
	if p.pos >= len(p.src) {
		return nil, false
	}
	switch c := p.src[p.pos]; {
	case c == '{':
		return p.parseObject()
	case c == '[':
		return p.parseArray()
	case c == '"':
		s, ok := p.parseString()
		if !ok {
			return nil, false
		}
		return &jsonValue{kind: jsonString, str: s}, true
	case c == 't':
		if strings.HasPrefix(p.src[p.pos:], "true") {
			p.pos += 4
			return &jsonValue{kind: jsonBool, str: "true"}, true
		}
		return nil, false
	case c == 'f':
		if strings.HasPrefix(p.src[p.pos:], "false") {
			p.pos += 5
			return &jsonValue{kind: jsonBool, str: "false"}, true
		}
		return nil, false
	case c == 'n':
		if strings.HasPrefix(p.src[p.pos:], "null") {
			p.pos += 4
			return &jsonValue{kind: jsonNull}, true
		}
		return nil, false
	default:
		return p.parseNumber()
	}
}

func (p *jsonParser) parseObject() (*jsonValue, bool) {
	p.pos++ // '{'
	out := &jsonValue{kind: jsonObject, object: map[string]*jsonValue{}}
	p.skipSpace()
	if p.pos < len(p.src) && p.src[p.pos] == '}' {
		p.pos++
		return out, true
	}
	for {
		p.skipSpace()
		key, ok := p.parseString()
		if !ok {
			return nil, false
		}
		p.skipSpace()
		if p.pos >= len(p.src) || p.src[p.pos] != ':' {
			return nil, false
		}
		p.pos++
		val, ok := p.parseValue()
		if !ok {
			return nil, false
		}
		out.object[key] = val
		p.skipSpace()
		if p.pos >= len(p.src) {
			return nil, false
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
		case '}':
			p.pos++
			return out, true
		default:
			return nil, false
		}
	}
}

func (p *jsonParser) parseArray() (*jsonValue, bool) {
	p.pos++ // '['
	out := &jsonValue{kind: jsonArray}
	p.skipSpace()
	if p.pos < len(p.src) && p.src[p.pos] == ']' {
		p.pos++
		return out, true
	}
	for {
		val, ok := p.parseValue()
		if !ok {
			return nil, false
		}
		out.array = append(out.array, val)
		p.skipSpace()
		if p.pos >= len(p.src) {
			return nil, false
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return out, true
		default:
			return nil, false
		}
	}
}

func (p *jsonParser) parseString() (string, bool) {
	if p.pos >= len(p.src) || p.src[p.pos] != '"' {
		return "", false
	}
	p.pos++
	var b strings.Builder
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch c {
		case '"':
			p.pos++
			return b.String(), true
		case '\\':
			p.pos++
			if p.pos >= len(p.src) {
				return "", false
			}
			switch p.src[p.pos] {
			case '"', '\\', '/':
				b.WriteByte(p.src[p.pos])
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'u':
				// The plugin only reads identifiers and model names; a
				// surrogate pair is not worth the code, so a non-ASCII
				// escape is dropped rather than mangled.
				if p.pos+4 >= len(p.src) {
					return "", false
				}
				p.pos += 4
			default:
				return "", false
			}
			p.pos++
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
	return "", false
}

func (p *jsonParser) parseNumber() (*jsonValue, bool) {
	start := p.pos
	if p.pos < len(p.src) && (p.src[p.pos] == '-' || p.src[p.pos] == '+') {
		p.pos++
	}
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if (c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' || c == '-' || c == '+' {
			p.pos++
			continue
		}
		break
	}
	if p.pos == start {
		return nil, false
	}
	raw := p.src[start:p.pos]
	n, ok := parseFloat(raw)
	if !ok {
		return nil, false
	}
	return &jsonValue{kind: jsonNumber, num: n}, true
}

// parseFloat parses the decimal spellings JSON allows, without strconv's
// full grammar (no hex, no inf/nan: neither is valid JSON).
func parseFloat(raw string) (float64, bool) {
	i := 0
	neg := false
	if i < len(raw) && raw[i] == '-' {
		neg = true
		i++
	}
	intPart := 0.0
	digits := 0
	for ; i < len(raw) && raw[i] >= '0' && raw[i] <= '9'; i++ {
		intPart = intPart*10 + float64(raw[i]-'0')
		digits++
	}
	fracPart := 0.0
	if i < len(raw) && raw[i] == '.' {
		i++
		scale := 0.1
		for ; i < len(raw) && raw[i] >= '0' && raw[i] <= '9'; i++ {
			fracPart += float64(raw[i]-'0') * scale
			scale /= 10
			digits++
		}
	}
	exp := 0
	if i < len(raw) && (raw[i] == 'e' || raw[i] == 'E') {
		i++
		expNeg := false
		if i < len(raw) && (raw[i] == '-' || raw[i] == '+') {
			expNeg = raw[i] == '-'
			i++
		}
		expDigits := 0
		for ; i < len(raw) && raw[i] >= '0' && raw[i] <= '9'; i++ {
			exp = exp*10 + int(raw[i]-'0')
			expDigits++
			if exp > 308 {
				return 0, false
			}
		}
		if expDigits == 0 {
			return 0, false
		}
		if expNeg {
			exp = -exp
		}
	}
	if i != len(raw) || digits == 0 {
		return 0, false
	}
	out := intPart + fracPart
	for ; exp > 0; exp-- {
		out *= 10
	}
	for ; exp < 0; exp++ {
		out /= 10
	}
	if neg {
		out = -out
	}
	return out, true
}

// jsonEscape writes s as a JSON string body (no surrounding quotes), so a
// model name or an error message cannot break the envelope.
func jsonEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				b.WriteString(` `)
				continue
			}
			b.WriteByte(c)
		}
	}
	return b.String()
}

// lastJSONObject finds the last complete top-level JSON object in s.
//
// A streaming (SSE) response is not JSON: it is a sequence of `data:` lines.
// The engine reports token usage in the final chunk, so scanning for the
// last balanced object is how the plugin reaches it without parsing SSE.
func lastJSONObject(s string) (*jsonValue, bool) {
	end := strings.LastIndexByte(s, '}')
	if end < 0 {
		return nil, false
	}
	depth := 0
	inString := false
	escaped := false
	for i := end; i >= 0; i-- {
		c := s[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '}':
			depth++
		case '{':
			depth--
			if depth == 0 {
				return parseJSON(s[i : end+1])
			}
		}
	}
	return nil, false
}
