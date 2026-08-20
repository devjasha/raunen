package config

import "testing"

// TestAddSkillsMergesWithJSON: both formats are supported at once, and code
// downstream sees one set without knowing where a skill came from.
func TestAddSkillsMergesWithJSON(t *testing.T) {
	c := &Config{Skills: map[string]Skill{
		"legacy": {Prompt: "From skills.json."},
	}}
	c.AddSkills(map[string]Skill{
		"review": {Prompt: "From SKILL.md.", Source: "/p/skills/review/SKILL.md"},
	})

	for _, name := range []string{"legacy", "review"} {
		if _, ok := c.Skill(name); !ok {
			t.Errorf("%q was lost in the merge", name)
		}
	}
}

// TestDiscoveredSkillWinsOverJSON: skills.json is the older, flatter format, so
// a directory written for a skill someone already had is the newer statement.
func TestDiscoveredSkillWinsOverJSON(t *testing.T) {
	c := &Config{Skills: map[string]Skill{
		"review": {Prompt: "Old."},
	}}
	c.AddSkills(map[string]Skill{
		"review": {Prompt: "New.", Source: "/p/skills/review/SKILL.md"},
	})

	s, _ := c.Skill("review")
	if s.Prompt != "New." {
		t.Errorf("prompt = %q, want the discovered skill to win", s.Prompt)
	}
	if s.Source == "" {
		t.Error("the discovered skill lost its source")
	}
}

// TestAddSkillsIntoAnEmptyConfig covers the nil map, which is what a config
// with no skills.json has.
func TestAddSkillsIntoAnEmptyConfig(t *testing.T) {
	c := &Config{}
	c.AddSkills(map[string]Skill{"x": {Prompt: "Body."}})
	if _, ok := c.Skill("x"); !ok {
		t.Error("adding to a config with no skills map lost the skill")
	}
}
