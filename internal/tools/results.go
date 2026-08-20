package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// A tool result used to be either small enough to hand to the model whole or
// large enough to be cut off and lost. Neither is right. A build log is four
// thousand lines of which six matter, and which six is not knowable until the
// model looks; a file read is worth having in full right up until it is not.
//
// So a large result is kept here and only its head goes into the conversation,
// with a handle for the rest. The model pays for a page and can ask for another
// page, or grep the whole thing, without the tokens for all of it sitting in
// every subsequent request for the rest of the session. That is the difference
// between a 32k window compacting twice during a build-fix loop and not
// compacting at all.
//
// The store is per-session and in memory. It is not a cache of anything on
// disk: a handle refers to what a command printed at a moment, and re-running
// the command is the only way to get a newer answer.

const (
	// storeMaxBytes bounds the whole store. Tool output is not small — a few
	// verbose test runs reach this — and the point is to keep it out of the
	// context, not to keep it forever.
	storeMaxBytes = 8 << 20
	// storeMaxItems bounds how many handles stay live. Older ones are dropped
	// first: the model asks for the tail of a result within a step or two of
	// seeing its head, or never.
	storeMaxItems = 64
)

// Store holds full tool results that were too large to put in the context.
//
// It is safe for concurrent use: delegated tasks run in parallel with each
// other and with the caller's own tool calls, and all of them store results.
type Store struct {
	mu    sync.Mutex
	seq   int
	items map[string]*stored
	order []string
	bytes int
}

type stored struct {
	tool  string
	lines []string
	bytes int
}

// NewStore returns an empty result store.
func NewStore() *Store {
	return &Store{items: map[string]*stored{}}
}

// Put files a result and returns the handle for it.
func (s *Store) Put(tool, text string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = map[string]*stored{}
	}
	s.seq++
	id := fmt.Sprintf("r%d", s.seq)
	s.items[id] = &stored{tool: tool, lines: strings.Split(text, "\n"), bytes: len(text)}
	s.order = append(s.order, id)
	s.bytes += len(text)
	// Evict from the front until back inside both bounds. Dropping the oldest
	// is the right order: a handle the model has not used within a few steps it
	// is not going to use.
	for len(s.order) > storeMaxItems || (s.bytes > storeMaxBytes && len(s.order) > 1) {
		old := s.order[0]
		s.order = s.order[1:]
		if r, ok := s.items[old]; ok {
			s.bytes -= r.bytes
			delete(s.items, old)
		}
	}
	return id
}

// Len reports how many results are currently held. For tests.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

func (s *Store) get(id string) (*stored, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.items[id]
	return r, ok
}

// pageBytes bounds one page fetched through a handle, so paging through a large
// result cannot undo the saving that storing it made in the first place.
func pageBytes(maxOutput int) int {
	return max(1<<10, maxOutput)
}

// addResult registers the tool that reads back what Put filed away.
func addResult(r *Registry, store *Store, maxOutput int) {
	r.Add(Tool{
		Name: "result",
		Description: "Read more of a large tool result that was cut short. " +
			"Takes the handle shown at the end of the truncated output. " +
			"Give match to search the whole result, or from and lines to page through it.",
		Params: obj(map[string]any{
			"id":    str("The handle from the truncated output, such as r1."),
			"match": str("Optional regular expression. Returns only matching lines, with their line numbers."),
			"from":  map[string]any{"type": "integer", "description": "First line to return (1-based). Default 1."},
			"lines": map[string]any{"type": "integer", "description": "How many lines to return. Default: as many as fit."},
		}, "id"),
		Run: func(_ context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				ID    string `json:"id"`
				Match string `json:"match"`
				From  int    `json:"from"`
				Lines int    `json:"lines"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			id := strings.TrimSpace(a.ID)
			res, ok := store.get(id)
			if !ok {
				// Say why plainly. A model told only "not found" tends to guess
				// at other handles; told the result is gone, it re-runs the
				// command, which is the correct move.
				return "", fmt.Errorf("no result %q: it was never stored, or it has been dropped to save memory. "+
					"Run the tool again to get a fresh result", id)
			}
			budget := pageBytes(maxOutput)
			if a.Match != "" {
				return grepResult(res, a.Match, budget)
			}
			return pageResult(res, a.From, a.Lines, budget)
		},
	})
}

// pageResult returns a window of lines, saying where in the whole it sits so
// the model can ask for the next window without guessing.
func pageResult(res *stored, from, count, budget int) (string, error) {
	total := len(res.lines)
	if from <= 0 {
		from = 1
	}
	if from > total {
		return "", fmt.Errorf("line %d is past the end; the result has %d lines", from, total)
	}
	start := from - 1
	end := total
	if count > 0 && start+count < end {
		end = start + count
	}

	var sb strings.Builder
	i := start
	for ; i < end; i++ {
		if sb.Len()+len(res.lines[i])+1 > budget && i > start {
			break
		}
		sb.WriteString(res.lines[i])
		sb.WriteByte('\n')
	}
	out := strings.TrimRight(sb.String(), "\n")
	header := fmt.Sprintf("[lines %d-%d of %d]\n", from, i, total)
	if i < total {
		return header + out + fmt.Sprintf("\n... [%d more lines; continue from line %d]", total-i, i+1), nil
	}
	return header + out, nil
}

// grepResult searches the whole stored result. This is the case the store
// exists for: the interesting six lines of a four thousand line build log are
// findable without ever spending the four thousand.
func grepResult(res *stored, pattern string, budget int) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("bad pattern %q: %w", pattern, err)
	}
	var sb strings.Builder
	hits, shown := 0, 0
	for i, line := range res.lines {
		if !re.MatchString(line) {
			continue
		}
		hits++
		if sb.Len()+len(line)+8 > budget {
			continue
		}
		fmt.Fprintf(&sb, "%d: %s\n", i+1, clip(line))
		shown++
	}
	if hits == 0 {
		return fmt.Sprintf("[no lines match %q in %d lines]", pattern, len(res.lines)), nil
	}
	out := strings.TrimRight(sb.String(), "\n")
	if shown < hits {
		return fmt.Sprintf("[%d matches, showing the first %d]\n", hits, shown) + out, nil
	}
	noun := "lines"
	if hits == 1 {
		noun = "line"
	}
	return fmt.Sprintf("[%d matching %s of %d]\n", hits, noun, len(res.lines)) + out, nil
}

// boundResults returns the function a registry uses to hold every tool result
// to a size.
//
// Cleaning comes first, so the budget is spent on content rather than on colour
// codes and progress bars — which also means the useful end of a long log is
// likelier to survive. What still does not fit is filed in the store rather
// than thrown away, and the handle goes into the result. The model sees a page
// and can fetch or search the rest; the tokens for the rest never enter the
// conversation at all, which is what keeps a few verbose commands from forcing
// a compaction.
func boundResults(store *Store, maxOutput int) func(tool, s string) string {
	return func(tool, s string) string {
		s = Clean(s)
		preview := PreviewBudget(maxOutput)
		if len(s) <= preview {
			return s
		}
		// Cut on a line boundary when there is one nearby: half a stack frame
		// is harder to read than one frame fewer.
		head := s[:preview]
		if i := strings.LastIndexByte(head, '\n'); i > preview/2 {
			head = head[:i]
		}
		id := store.Put(tool, s)
		rest := strings.Count(s[len(head):], "\n")
		return head + fmt.Sprintf(
			"\n... [%d more lines, %d bytes total. The whole result is kept as %s: "+
				"call result with id %q and a match pattern to search it, "+
				"or from and lines to read on.]", rest, len(s), id, id)
	}
}
