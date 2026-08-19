package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// A tool call that changes a file used to be one line: the marker, the tool
// name, and the path. That is enough to know something happened and not enough
// to know whether it was the right thing — the reader had to go and open the
// file to find out, which is exactly the review the transcript should be
// saving them.
//
// So a write or an edit also draws a small window of the code itself: the new
// content for a file that did not exist, and a unified-style diff for one that
// did. Only those two tools get a window. A read or a list has nothing to show
// that its result line does not already say, and a bash command's output is
// not code.
//
// Everything here works on the arguments the model sent plus a snapshot of the
// file taken before the tool ran, and nothing here is rendered until rows() is
// called with a width — see codeBlock for why that matters.

// diffKind is what a line is doing in the window.
type diffKind uint8

const (
	lineContext diffKind = iota // unchanged, shown to place the change
	lineAdd                     // present only after
	lineDel                     // present only before
	lineElide                   // stands in for a run of unchanged lines
)

// diffLine is one row of the window. The two numbers are the 1-based positions
// in the before and after files; a line that exists on only one side carries
// zero for the other, which is what lets the gutter show the number that
// actually means something for that row.
type diffLine struct {
	kind         diffKind
	text         string
	oldNo, newNo int
	// n is how many lines an elision stands for, unused otherwise.
	n int
}

const (
	// maxDiffCells caps the LCS table. The algorithm is O(n*m) in time and
	// memory, which is fine for the edits a model actually makes and ruinous
	// for a model that rewrites a ten-thousand-line file: that would be a
	// hundred million cells built on the UI goroutine, freezing the frame.
	// Past this size the window degrades to "all of the old, then all of the
	// new", which is still true, just less useful — and the hunk collapsing
	// below means only the first few lines are drawn anyway.
	maxDiffCells = 250_000

	// diffContext is how many unchanged lines are kept either side of a change.
	// Two is enough to see which function the change landed in without the
	// window turning into the file.
	diffContext = 2

	// maxBodyLines is the most rows of code the window will draw. The window
	// sits inside a scrolling transcript, so a big one pushes the conversation
	// off the screen; anything longer than this is better read in the file.
	maxBodyLines = 16
)

// lineDiff computes a line-based diff by longest common subsequence.
//
// The naive alternative — "everything in old was removed, everything in new
// was added" — is what a first cut does, and it is unreadable for the common
// case of a model changing one line in the middle of a block it quoted for
// context: every line comes out as a delete paired with an identical add. The
// LCS keeps the lines both sides agree on as context, so what is coloured is
// what actually changed.
func lineDiff(oldLines, newLines []string) []diffLine {
	n, m := len(oldLines), len(newLines)
	if n*m > maxDiffCells {
		return flatDiff(oldLines, newLines)
	}

	// dp[i][j] is the length of the LCS of oldLines[i:] and newLines[j:]. It is
	// filled from the end so the walk below can go forwards and emit lines in
	// file order rather than reversing them afterwards.
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}

	out := make([]diffLine, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldLines[i] == newLines[j]:
			out = append(out, diffLine{kind: lineContext, text: oldLines[i], oldNo: i + 1, newNo: j + 1})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			// Ties go to the deletion, so a changed line reads as "- old" then
			// "+ new", the order every diff tool in the world uses.
			out = append(out, diffLine{kind: lineDel, text: oldLines[i], oldNo: i + 1})
			i++
		default:
			out = append(out, diffLine{kind: lineAdd, text: newLines[j], newNo: j + 1})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, diffLine{kind: lineDel, text: oldLines[i], oldNo: i + 1})
	}
	for ; j < m; j++ {
		out = append(out, diffLine{kind: lineAdd, text: newLines[j], newNo: j + 1})
	}
	return out
}

// flatDiff is the fallback for inputs too large to align: everything old is
// removed and everything new is added.
func flatDiff(oldLines, newLines []string) []diffLine {
	out := make([]diffLine, 0, len(oldLines)+len(newLines))
	for i, l := range oldLines {
		out = append(out, diffLine{kind: lineDel, text: l, oldNo: i + 1})
	}
	for j, l := range newLines {
		out = append(out, diffLine{kind: lineAdd, text: l, newNo: j + 1})
	}
	return out
}

// collapse replaces runs of unchanged lines that are further than ctx from any
// change with a single marker saying how many were dropped. Without it, an edit
// near the end of a quoted block spends the whole window on lines nobody needs
// to read and then runs out of room before the change.
func collapse(lines []diffLine, ctx int) []diffLine {
	keep := make([]bool, len(lines))
	for i, l := range lines {
		if l.kind == lineContext {
			continue
		}
		for j := max(0, i-ctx); j <= min(len(lines)-1, i+ctx); j++ {
			keep[j] = true
		}
	}

	out := make([]diffLine, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if keep[i] {
			out = append(out, lines[i])
			continue
		}
		j := i
		for j < len(lines) && !keep[j] {
			j++
		}
		out = append(out, diffLine{kind: lineElide, n: j - i})
		i = j - 1
	}
	return out
}

// codeBlock is a code window as data rather than as text.
//
// Storing the rendered strings would be simpler and wrong: the transcript
// re-renders every entry from its own fields whenever the terminal is resized
// (see Model.rewrap), and a block of pre-formatted rows would keep the border
// and the truncation of the width it was built at, leaving ragged edges down
// the screen the moment the window changes size. Everything width-dependent
// happens in rows(), which rewrap calls again with the new width.
type codeBlock struct {
	// path is shown as given by the model, so it matches the tool line above.
	path string
	// label is the summary beside the path — the change counts, or a note that
	// the file is new.
	label string
	// indent lines the window up under the tool call that produced it.
	indent string
	lines  []diffLine
	// numbered records that the line numbers are real. An edit whose old text
	// could not be located in the file is still worth showing as a diff, but
	// numbering it would be making positions up.
	numbered bool
}

var (
	codeAdd    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	codeDel    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	codeCtx    = lipgloss.NewStyle().Foreground(lipgloss.Color("7")).Faint(true)
	codeGutter = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Faint(true)
	codeBorder = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	codeHead   = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Faint(true)
)

// rows draws the window at the given width, which is the transcript's inner
// width. Every line is truncated rather than wrapped: code that wraps loses the
// column alignment that makes a diff readable, and a window whose rows are all
// one line deep costs a predictable number of screen rows.
func (c *codeBlock) rows(width int) []string {
	return c.rowsCapped(width, maxBodyLines)
}

// rowsCapped is rows with the body limit given rather than assumed. The
// sub-agent panel draws the same windows a few rows tall inside its own border,
// where the transcript's sixteen would be the entire panel and then some, so it
// asks for as many lines as it can spare instead of taking the default.
func (c *codeBlock) rowsCapped(width, maxBody int) []string {
	if c == nil || len(c.lines) == 0 || maxBody < 1 {
		return nil
	}
	indentW := lipgloss.Width(c.indent)
	// Four cells go to the frame: a border and a pad on each side.
	inner := max(8, width-indentW-4)

	body := collapse(c.lines, diffContext)
	over := 0
	if len(body) > maxBody {
		// The caller asked for maxBody rows and meant it — the transcript hands
		// over its own limit, the sub-agent panel a much smaller one — so the
		// truncation respects that number rather than the default. The lines cut
		// off here are not lost silently: the "more lines" marker counts them.
		over = len(body) - maxBody
		body = body[:maxBody]
	}

	// The gutter is as wide as the largest number it has to hold, so the code
	// starts at the same column on every row.
	numW := 0
	if c.numbered {
		for _, l := range body {
			numW = max(numW, len(fmt.Sprint(max(l.oldNo, l.newNo))))
		}
	}

	out := make([]string, 0, len(body)+3)
	out = append(out, c.header(inner))
	for _, l := range body {
		out = append(out, c.row(l, numW, inner))
	}
	if over > 0 {
		out = append(out, codeGutter.Render(ansi.Truncate(
			fmt.Sprintf("… %d more lines", over), inner, "")))
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(codeBorder.GetForeground()).
		Padding(0, 1).
		Width(inner + 2).
		Render(strings.Join(out, "\n"))

	rows := strings.Split(box, "\n")
	for i, r := range rows {
		rows[i] = c.indent + r
	}
	return rows
}

// header names the file and says how much moved. The path is what identifies
// the change, so the label is shed first when the window is narrow.
func (c *codeBlock) header(inner int) string {
	label := c.label
	if lipgloss.Width(label)+2 >= inner {
		label = ""
	}
	room := inner
	if label != "" {
		room -= lipgloss.Width(label) + 2
	}
	head := codeHead.Render(ansi.Truncate(c.path, max(3, room), "…"))
	if label != "" {
		head += codeGutter.Render("  " + label)
	}
	return head
}

// row draws one line of code: the number, the +/-/space marker, and the text.
func (c *codeBlock) row(l diffLine, numW, inner int) string {
	if l.kind == lineElide {
		return codeGutter.Render(ansi.Truncate(
			fmt.Sprintf("⋯ %d unchanged lines", l.n), inner, ""))
	}

	num := ""
	if c.numbered {
		n := max(l.oldNo, l.newNo)
		num = fmt.Sprintf("%*d ", numW, n)
	}

	mark, style := " ", codeCtx
	switch l.kind {
	case lineAdd:
		mark, style = "+", codeAdd
	case lineDel:
		mark, style = "-", codeDel
	}

	gutter := num + mark + " "
	// Tabs are a single cell to the terminal but not to the eye, and one tab in
	// a diff row throws the whole column alignment out. Two spaces is enough to
	// show the nesting without spending the width eight at a time.
	text := strings.ReplaceAll(l.text, "\t", "  ")
	text = ansi.Truncate(text, max(1, inner-lipgloss.Width(gutter)), "…")
	return codeGutter.Render(num) + style.Render(mark+" "+text)
}

// codeWindow builds the window for a tool call, or nil when the call is not one
// that shows code — which is most of them.
//
// It must be called before the tool runs. The whole point of the diff is the
// contents the file had beforehand, and by the time the result comes back the
// file on disk is already the new version, so calling this at ToolEnd would
// diff a file against itself and draw an empty window every time.
func codeWindow(root, name, args, indent string) *codeBlock {
	switch name {
	case "write":
		var a struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(args), &a); err != nil || strings.TrimSpace(a.Path) == "" {
			return nil
		}
		return writeWindow(root, a.Path, a.Content, indent)
	case "edit":
		var a struct {
			Path string `json:"path"`
			Old  string `json:"old"`
			New  string `json:"new"`
		}
		if err := json.Unmarshal([]byte(args), &a); err != nil || strings.TrimSpace(a.Path) == "" {
			return nil
		}
		return editWindow(root, a.Path, a.Old, a.New, indent)
	default:
		return nil
	}
}

// writeWindow shows the new content for a file that does not exist yet, and a
// diff against what is on disk for one that does. A write over an existing file
// is the destructive case — it is worth seeing what it replaced.
func writeWindow(root, path, content, indent string) *codeBlock {
	before, existed := readFileFor(root, path)
	newLines := codeLines(content)
	if !existed {
		lines := make([]diffLine, len(newLines))
		for i, l := range newLines {
			lines[i] = diffLine{kind: lineAdd, text: l, newNo: i + 1}
		}
		return &codeBlock{
			path:     path,
			label:    fmt.Sprintf("new file  +%d", len(lines)),
			indent:   indent,
			lines:    lines,
			numbered: true,
		}
	}
	lines := lineDiff(codeLines(before), newLines)
	return block(path, indent, lines, true)
}

// editWindow diffs the replaced text against its replacement rather than the
// whole file. The file is usually far larger than the edit, so diffing all of
// it would spend an O(n*m) table on lines that cannot have changed — and would
// be the input most likely to trip the size guard and lose the alignment that
// makes the window worth drawing.
//
// The snippet is widened to whole lines on both ends so a replacement that
// starts or ends mid-line still lines up with the file, and the line numbers
// are offset by where it sits.
func editWindow(root, path, oldText, newText, indent string) *codeBlock {
	before, existed := readFileFor(root, path)
	idx := -1
	if existed {
		idx = strings.Index(before, oldText)
	}
	if idx < 0 {
		// Either the file could not be read or the model's old text is not in
		// it — the tool is about to fail. Showing the intended change is still
		// the most useful thing available, but with nothing to number it from.
		b := block(path, indent, lineDiff(codeLines(oldText), codeLines(newText)), false)
		return b
	}

	start := strings.LastIndex(before[:idx], "\n") + 1
	end := idx + len(oldText)
	if n := strings.IndexByte(before[end:], '\n'); n >= 0 {
		end += n
	} else {
		end = len(before)
	}
	oldSnip := before[start:end]
	newSnip := before[start:idx] + newText + before[idx+len(oldText):end]

	base := strings.Count(before[:start], "\n")
	lines := lineDiff(codeLines(oldSnip), codeLines(newSnip))
	for i := range lines {
		if lines[i].oldNo > 0 {
			lines[i].oldNo += base
		}
		if lines[i].newNo > 0 {
			lines[i].newNo += base
		}
	}
	return block(path, indent, lines, true)
}

// block wraps a diff up with its counts, and refuses to draw a window for a
// change that turned out to be no change at all.
func block(path, indent string, lines []diffLine, numbered bool) *codeBlock {
	added, removed := 0, 0
	for _, l := range lines {
		switch l.kind {
		case lineAdd:
			added++
		case lineDel:
			removed++
		}
	}
	if added == 0 && removed == 0 {
		return nil
	}
	return &codeBlock{
		path:     path,
		label:    fmt.Sprintf("+%d -%d", added, removed),
		indent:   indent,
		lines:    lines,
		numbered: numbered,
	}
}

// readFileFor reads the file a tool is about to touch, resolving the path the
// same way the tools do — relative to the session root — so the window is
// looking at the file the tool will write.
func readFileFor(root, path string) (string, bool) {
	p := path
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// codeLines splits text into lines, dropping the empty element a trailing
// newline leaves behind: it is not a line of the file, and counting it makes
// every window claim one addition more than the change contains.
func codeLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}
