// Package session stores conversations on disk so they can be resumed.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"raunen/internal/provider"
)

// Session is one conversation, including everything needed to carry it on.
type Session struct {
	ID       string             `json:"id"`
	Title    string             `json:"title"`
	Model    string             `json:"model"`
	Root     string             `json:"root"`
	Created  time.Time          `json:"created"`
	Updated  time.Time          `json:"updated"`
	Messages []provider.Message `json:"messages"`
}

// Dir is where sessions are kept, following XDG rather than
// os.UserConfigDir — these are data, not configuration.
func Dir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "raunen", "sessions")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "raunen-sessions"
	}
	return filepath.Join(home, ".local", "share", "raunen", "sessions")
}

// New starts a session. It is not written to disk until it has something worth
// keeping; see Save.
func New(root, model string) *Session {
	now := time.Now()
	return &Session{
		// Time-ordered so the filename sorts the way the list does, with a
		// suffix so two sessions started in the same second cannot collide.
		ID:      fmt.Sprintf("%s-%04x", now.Format("20060102-150405"), now.UnixNano()&0xffff),
		Model:   model,
		Root:    root,
		Created: now,
		Updated: now,
	}
}

func (s *Session) path() string { return filepath.Join(Dir(), s.ID+".json") }

// Save writes the session out. A conversation with nothing but a system prompt
// is dropped rather than saved, so merely starting the program does not litter
// the store with empty sessions.
func (s *Session) Save() error {
	if len(s.Messages) < 2 {
		return nil
	}
	s.Updated = time.Now()
	if s.Title == "" {
		s.Title = titleFrom(s.Messages)
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// Written via a temporary file so an interrupted save cannot leave a
	// half-written session behind.
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}

// titleFrom takes the first thing the user actually asked.
func titleFrom(msgs []provider.Message) string {
	for _, m := range msgs {
		if m.Role != provider.User {
			continue
		}
		t := strings.Join(strings.Fields(m.Content), " ")
		if len(t) > 60 {
			t = t[:57] + "…"
		}
		return t
	}
	return "untitled"
}

// Load reads a session by id.
func Load(id string) (*Session, error) {
	b, err := os.ReadFile(filepath.Join(Dir(), id+".json"))
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("reading session %s: %w", id, err)
	}
	return &s, nil
}

// List returns saved sessions, newest first. When root is non-empty only
// sessions started in that directory are returned.
func List(root string, limit int) ([]*Session, error) {
	entries, err := os.ReadDir(Dir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out []*Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		s, err := Load(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			// One unreadable file should not hide the rest.
			continue
		}
		if root != "" && s.Root != root {
			continue
		}
		out = append(out, s)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Latest returns the most recent session for a directory, or nil if there is
// none.
func Latest(root string) (*Session, error) {
	list, err := List(root, 1)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return list[0], nil
}

// Age renders how long ago the session was touched, for listings.
func (s *Session) Age() string {
	d := time.Since(s.Updated)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// Turns counts the user messages, which is a more useful size than the raw
// message count.
func (s *Session) Turns() int {
	n := 0
	for _, m := range s.Messages {
		if m.Role == provider.User {
			n++
		}
	}
	return n
}
