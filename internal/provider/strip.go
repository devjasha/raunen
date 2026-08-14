package provider

import (
	"strings"
)

// Local models frequently leak their tool-call scaffolding into ordinary
// content — emitting a proper structured tool call and *also* spilling tags
// like </tool_call> or </parameter> as text. Rendering that is noise, so it is
// filtered out of the content stream.
//
// Only these specific names are dropped. A tag that is not on the list is
// passed through untouched, so a model writing HTML or XML is unaffected.
var markupTags = []string{
	"tool_call", "tool_response", "tool_result", "tool_use",
	"function", "function_call", "parameter", "parameters",
	"think", "thinking", "reasoning", "antml",
}

// isMarkup reports whether a complete tag such as "</tool_call>" or
// "<function=foo>" is tool-call scaffolding rather than content.
func isMarkup(tag string) bool {
	s := strings.TrimSuffix(strings.TrimPrefix(tag, "<"), ">")
	s = strings.TrimPrefix(s, "/")
	// Some templates wrap names in pipes, as in <|tool_call|>.
	s = strings.Trim(s, "|")
	s = strings.TrimSuffix(s, "/")
	// Keep only the tag name: drop attributes and the "=value" form.
	if i := strings.IndexAny(s, " \t\n="); i >= 0 {
		s = s[:i]
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return false
	}
	for _, t := range markupTags {
		if s == t {
			return true
		}
	}
	return false
}

// maxTagLen bounds how much text is held back while deciding whether a '<'
// begins a tag. Beyond this it cannot be one of the names above.
const maxTagLen = 32

// stripper filters markup out of a stream of content deltas. Text arrives a
// token at a time, so a tag can be split across several deltas; the stripper
// holds back a candidate until it can decide, then releases or drops it.
type stripper struct {
	pending strings.Builder // text from '<' onward, not yet resolved
}

// Feed consumes a delta and returns the text safe to emit now.
func (s *stripper) Feed(in string) string {
	var out strings.Builder

	for _, r := range in {
		if s.pending.Len() > 0 {
			s.pending.WriteRune(r)
			p := s.pending.String()

			if r == '>' {
				if !isMarkup(p) {
					out.WriteString(p)
				}
				s.pending.Reset()
				continue
			}
			// A second '<' means the first was literal text, not a tag.
			if r == '<' {
				out.WriteString(p[:len(p)-1])
				s.pending.Reset()
				s.pending.WriteRune('<')
				continue
			}
			if s.pending.Len() > maxTagLen {
				out.WriteString(p)
				s.pending.Reset()
			}
			continue
		}

		if r == '<' {
			s.pending.WriteRune('<')
			continue
		}
		out.WriteRune(r)
	}

	return out.String()
}

// Flush releases any held-back text at end of stream. An unterminated
// candidate was never a tag, so it is emitted as literal content.
func (s *stripper) Flush() string {
	p := s.pending.String()
	s.pending.Reset()
	return p
}
