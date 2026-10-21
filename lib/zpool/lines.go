package zpool

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// line is one line zpool_influxdb writes, in InfluxDB's line protocol: what is measured, the tags that say of what, and the values.
type line struct {
	measurement string
	tags        map[string]string
	fields      map[string]string
	// names is the values' names in the order written
	names []string
}

// count is a value as a count: zpool_influxdb writes each with a u after it, or an i when asked for signed numbers. It is false for a value that is
// not a whole number, or not there.
func (l *line) count(field string) (uint64, bool) {
	value, found := l.fields[field]
	if !found {
		return 0, false
	}

	n, err := strconv.ParseUint(strings.TrimRight(value, "ui"), 10, 64)

	return n, err == nil
}

// parseLine reads one line: measurement,tag=value,tag=value field=value,field=value timestamp. A backslash keeps the comma, space or equals sign
// after it as part of a name or a value, as a path with a space in it needs.
func parseLine(text string) (line, error) {
	parts := split(text, ' ')
	if len(parts) < 2 {
		return line{}, fmt.Errorf("%q is not a measurement and its values", text)
	}

	head := split(parts[0], ',')
	l := line{measurement: unescape(head[0]), tags: map[string]string{}, fields: map[string]string{}}
	if l.measurement == "" {
		return line{}, fmt.Errorf("%q names no measurement", text)
	}

	for _, pair := range head[1:] {
		key, value, err := pairOf(pair)
		if err != nil {
			return line{}, fmt.Errorf("a tag of %q: %w", text, err)
		}
		l.tags[key] = value
	}

	for _, pair := range split(parts[1], ',') {
		key, value, err := pairOf(pair)
		if err != nil {
			return line{}, fmt.Errorf("a value of %q: %w", text, err)
		}
		l.fields[key] = value
		l.names = append(l.names, key)
	}

	return l, nil
}

// pairOf is a name and its value, written name=value.
func pairOf(pair string) (name, value string, err error) {
	halves := split(pair, '=')
	if len(halves) < 2 || halves[0] == "" {
		return "", "", errors.New("it is not written name=value")
	}

	return unescape(halves[0]), unescape(strings.Join(halves[1:], "=")), nil
}

// split cuts s at each sep that is not after a backslash or inside double quotes, leaving the backslashes in.
func split(s string, sep byte) []string {
	var parts []string
	start, quoted := 0, false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\':
			i++
		case s[i] == '"':
			quoted = !quoted
		case s[i] == sep && !quoted:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}

	return append(parts, s[start:])
}

// unescape drops the backslash before each character it keeps, and the quotes around a string value.
func unescape(s string) string {
	if unquoted, err := strconv.Unquote(s); err == nil && strings.HasPrefix(s, `"`) {
		return unquoted
	}

	if !strings.Contains(s, `\`) {
		return s
	}

	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}

	return b.String()
}
