// Package meta extracts a skill's short name and description from its document.
//
// Tolerant precedence: frontmatter when present, derivation otherwise.
// No skill is ever rejected for lacking metadata.
//
// Deliberate decision: there is no YAML parser here. delivery reads exactly two
// scalar fields from a document that came from an untrusted source, and YAML
// parsers have a history of vulnerabilities. The minimal parser below
// recognises scalar "key: value" pairs; nested structures, lists and
// multi-line scalars simply do not yield a field, and extraction falls back to
// derivation — which is the behaviour wanted here anyway.
package meta

import (
	"bufio"
	"bytes"
	"strings"
	"unicode"

	"github.com/kleberS4/delivery/internal/artifact"
	"github.com/kleberS4/delivery/internal/ref"
)

// MaxNameLen bounds the short name.
const MaxNameLen = 64

// MaxDescriptionLen bounds the description shown in the anchor index.
const MaxDescriptionLen = 200

// DefaultDescription is used when there is neither a frontmatter description
// nor a heading to derive one from. The description is never empty.
const DefaultDescription = "Skill with no description declared at the source."

// SplitFrontmatter separates frontmatter from body while preserving raw bytes.
//
// The split preserves bytes instead of re-serialising from a map, so raw+body
// is exactly the original document, by construction rather than by effort.
func SplitFrontmatter(doc []byte) (raw, body []byte, ok bool) {
	if !hasFenceAt(doc, 0) {
		return nil, doc, false
	}
	// Move past the opening fence.
	i := lineEnd(doc, 0)
	for i < len(doc) {
		if hasFenceAt(doc, i) {
			end := lineEnd(doc, i)
			return doc[:end], doc[end:], true
		}
		i = lineEnd(doc, i)
	}
	// Opening fence with no closing fence: not frontmatter.
	return nil, doc, false
}

// hasFenceAt reports whether the line starting at i is exactly the "---" fence.
func hasFenceAt(doc []byte, i int) bool {
	if i >= len(doc) {
		return false
	}
	end := lineEnd(doc, i)
	line := bytes.TrimRight(doc[i:end], "\r\n")
	return string(bytes.TrimRight(line, " \t")) == "---"
}

// lineEnd returns the index just past the end of the line starting at i.
func lineEnd(doc []byte, i int) int {
	j := bytes.IndexByte(doc[i:], '\n')
	if j < 0 {
		return len(doc)
	}
	return i + j + 1
}

// ParseFields extracts scalar pairs from raw frontmatter.
func ParseFields(raw []byte) map[string]string {
	fields := make(map[string]string)
	if len(raw) == 0 {
		return fields
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "---" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// An indented line belongs to a nested structure: skipped by declared
		// decision.
		if line != strings.TrimLeft(line, " \t") {
			continue
		}
		i := strings.Index(trimmed, ":")
		if i <= 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:i])
		val := strings.TrimSpace(trimmed[i+1:])
		if val == "" || val == "|" || val == ">" {
			continue
		}
		val = unquote(val)
		if key != "" && val != "" {
			fields[strings.ToLower(key)] = val
		}
	}
	return fields
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// Extract applies the metadata precedence to a document.
func Extract(doc []byte, r ref.Ref) artifact.Metadata {
	raw, body, ok := SplitFrontmatter(doc)

	var name, desc string
	origin := artifact.OriginDerived
	if ok {
		fields := ParseFields(raw)
		name = fields["name"]
		desc = fields["description"]
		if name != "" || desc != "" {
			origin = artifact.OriginFrontmatter
		}
	}

	if name == "" {
		name = r.BaseName()
		origin = artifact.OriginDerived
	}
	if desc == "" {
		desc = DeriveDescription(body)
		if origin == artifact.OriginFrontmatter && name != "" {
			// Authored name, derived description: the weaker origin wins, so
			// the listing never suggests the description came from the author.
			origin = artifact.OriginDerived
		}
	}

	return artifact.Metadata{
		Name:        NormalizeName(name),
		Description: truncate(desc, MaxDescriptionLen),
		Origin:      origin,
	}
}

// DeriveDescription takes the first heading of the body, or returns the
// default text when there is none.
func DeriveDescription(body []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#") {
			h := strings.TrimSpace(strings.TrimLeft(line, "#"))
			if h != "" {
				return h
			}
		}
	}
	return DefaultDescription
}

// NormalizeName produces a valid short name: lower case, restricted
// character set, no repeated separators, bounded length.
func NormalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := true // prevents a leading hyphen
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) && r < unicode.MaxASCII,
			unicode.IsDigit(r) && r < unicode.MaxASCII,
			r == '.', r == '_':
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == ' ' || r == '/':
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		default:
			// Characters outside the allowed set are dropped.
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > MaxNameLen {
		out = strings.Trim(out[:MaxNameLen], "-")
	}
	if out == "" {
		out = "skill"
	}
	return out
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n-1]) + "…"
}
