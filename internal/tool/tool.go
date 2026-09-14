// Package tool adapts delivery to wherever each AI tool keeps its skills.
//
// It is one of the three extension points. Codex and Gemini arrive as
// new Adapter implementations; the --tool flag already accepts the parameter
// in v1, so the command-line interface does not change when they land.
//
// Adapters do not convert formats: the markdown is written exactly as it came.
package tool

import (
	"os"
	"path/filepath"

	"github.com/kleberS4/delivery/internal/atomicfs"
	"github.com/kleberS4/delivery/internal/errs"
)

// EnvClaudeHome overrides the Claude Code root. Used in tests.
const EnvClaudeHome = "DELIVERY_CLAUDE_HOME"

// AnchorSkillName is the directory name of the anchor skill.
const AnchorSkillName = "delivery"

const skillFileName = "SKILL.md"

// Adapter is the interface implemented by each target tool.
type Adapter interface {
	Name() string
	SkillsDir() string
	AnchorPath() string
	SkillPath(name string) string
	WriteSkill(name string, content []byte) error
	RemoveSkill(name string) error
	ReadSkill(name string) ([]byte, error)
}

// Supported lists the names accepted by the --tool flag in v1.
func Supported() []string { return []string{"claude"} }

// ByName resolves an adapter by name.
func ByName(name string) (Adapter, error) {
	switch name {
	case "claude", "claude-code", "":
		return newClaudeCode()
	default:
		return nil, errs.Usage(
			"use --tool claude; other tools arrive in future versions",
			"tool %q is not supported in this version", name)
	}
}

type claudeCode struct {
	skillsDir string
}

func newClaudeCode() (Adapter, error) {
	root := os.Getenv(EnvClaudeHome)
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, errs.Wrap(err, errs.ClassState,
				"set "+EnvClaudeHome+" to the Claude Code root",
				"could not determine the user's home directory")
		}
		root = filepath.Join(home, ".claude")
	}
	return &claudeCode{skillsDir: filepath.Join(root, "skills")}, nil
}

func (c *claudeCode) Name() string      { return "claude" }
func (c *claudeCode) SkillsDir() string { return c.skillsDir }

func (c *claudeCode) AnchorPath() string {
	return filepath.Join(c.skillsDir, AnchorSkillName, skillFileName)
}

func (c *claudeCode) SkillPath(name string) string {
	return filepath.Join(c.skillsDir, name, skillFileName)
}

func (c *claudeCode) WriteSkill(name string, content []byte) error {
	if err := atomicfs.WriteFile(c.SkillPath(name), content, 0o600); err != nil {
		return errs.Wrap(err, errs.ClassState,
			"check permissions on "+c.skillsDir, "writing skill %q", name)
	}
	return nil
}

func (c *claudeCode) RemoveSkill(name string) error {
	dir := filepath.Dir(c.SkillPath(name))
	if err := os.RemoveAll(dir); err != nil {
		return errs.Wrap(err, errs.ClassState,
			"check permissions on "+c.skillsDir, "removing skill %q", name)
	}
	return nil
}

func (c *claudeCode) ReadSkill(name string) ([]byte, error) {
	b, err := os.ReadFile(c.SkillPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errs.NotFound("check the installed skill's name",
				"skill %q is not installed", name)
		}
		return nil, errs.Wrap(err, errs.ClassState, "check permissions",
			"reading skill %q", name)
	}
	return b, nil
}
