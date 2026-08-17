package ui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// keyPrompt asks for an API key when one is needed and missing.
//
// Choosing a model whose provider has no key would otherwise fail with a 401
// several seconds later, at which point the useful moment has passed. Asking at
// the point of choosing turns a dead end into one line of typing.
type keyPrompt struct {
	provider string
	// env is the variable the provider would rather read, mentioned so the
	// choice between typing it here and exporting it stays visible.
	env   string
	input textinput.Model
	// then is the model to switch to once a key exists, empty when the prompt
	// was opened on its own.
	then string
	// reopen asks for the model chooser back afterwards, since a new key
	// usually means new models to choose from.
	reopen bool
	err    string
}

var keyBorder = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))

func newKeyPrompt(providerName, env, then string) *keyPrompt {
	ti := textinput.New()
	ti.Prompt = "› "
	// No placeholder: the mask is applied to it too, so it renders as a single
	// stray character. The hint goes in the heading instead.
	// Masked: a key on screen ends up in screenshots and over shoulders.
	ti.EchoMode = textinput.EchoPassword
	ti.SetVirtualCursor(false)

	st := ti.Styles()
	st.Focused.Prompt = promptStyle
	st.Focused.Placeholder = dimStyle
	st.Focused.Text = lipgloss.NewStyle()
	ti.SetStyles(st)
	ti.Focus()

	return &keyPrompt{provider: providerName, env: env, input: ti, then: then}
}

// height is the rows the prompt occupies.
func (k *keyPrompt) height() int { return 5 }

func (k *keyPrompt) render(width int) string {
	inner := max(20, width-4)

	var b strings.Builder
	b.WriteString(askStyle.Render("api key for "+k.provider) +
		dimStyle.Render("  — paste it and press enter") + "\n")
	if k.err != "" {
		b.WriteString(errStyle.Render(ansi.Truncate(k.err, inner, "…")) + "\n")
	} else {
		b.WriteString(dimStyle.Render(ansi.Truncate(
			"saved to the config, which is written 0600 — or set "+k.env+" instead",
			inner, "…")) + "\n")
	}
	b.WriteString(k.input.View() + "\n")
	b.WriteString(dimStyle.Render("enter to save  ·  esc to cancel"))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(keyBorder.GetForeground()).
		Padding(0, 1).
		Width(max(10, width-2)).
		Render(b.String())
}
