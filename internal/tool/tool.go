// Package tool adapts delivery to wherever each AI tool keeps its skills.
//
// It is one of the three extension points. The --tool flag already
// accepted the parameter in v1, so adding a tool changes nothing about the
// command-line interface.
//
// Adapters do not convert formats: the markdown is written exactly as it came.
package tool

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/kleberS4/delivery/internal/atomicfs"
	"github.com/kleberS4/delivery/internal/errs"
)

// Roots overridden in tests, and by anyone whose tool lives somewhere unusual.
const (
	EnvClaudeHome = "DELIVERY_CLAUDE_HOME"
	EnvCodexHome  = "DELIVERY_CODEX_HOME"
)

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
	WriteResource(skill, relPath string, content []byte) error
	RemoveSkill(name string) error
	ReadSkill(name string) ([]byte, error)
}

// Supported lists the names accepted by the --tool flag.
func Supported() []string { return []string{"claude", "codex"} }

// ByName resolves an adapter by name.
//
// Codex reads personal skills from ~/.agents/skills, which is deliberately not
// under CODEX_HOME: that variable points at ~/.codex, where Codex keeps config,
// credentials and history — not skills. Pointing this at ~/.codex/skills would
// write somewhere Codex does not read.
func ByName(name string) (Adapter, error) {
	switch name {
	case "claude", "claude-code", "":
		return newDirAdapter("claude", EnvClaudeHome, ".claude")
	case "codex":
		return newDirAdapter("codex", EnvCodexHome, ".agents")
	default:
		return nil, errs.Usage(
			"use one of: "+strings.Join(Supported(), ", "),
			"tool %q is not supported", name)
	}
}

// dirAdapter serves any tool that keeps skills as one directory per skill with
// a SKILL.md inside it. Both tools supported so far do exactly that, and differ
// only in where that directory lives — so the behaviour is written once. A tool
// that stores skills some other way needs its own implementation of Adapter,
// not another root passed to this one.
type dirAdapter struct {
	name      string
	skillsDir string
}

func newDirAdapter(name, env, defaultDir string) (Adapter, error) {
	root := os.Getenv(env)
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, errs.Wrap(err, errs.ClassState,
				"set "+env+" to the "+name+" root",
				"could not determine the user's home directory")
		}
		root = filepath.Join(home, defaultDir)
	}
	return &dirAdapter{name: name, skillsDir: filepath.Join(root, "skills")}, nil
}

func (d *dirAdapter) Name() string      { return d.name }
func (d *dirAdapter) SkillsDir() string { return d.skillsDir }

func (d *dirAdapter) AnchorPath() string {
	return filepath.Join(d.skillsDir, AnchorSkillName, skillFileName)
}

func (d *dirAdapter) SkillPath(name string) string {
	return filepath.Join(d.skillsDir, name, skillFileName)
}

func (d *dirAdapter) WriteSkill(name string, content []byte) error {
	if err := atomicfs.WriteFile(d.SkillPath(name), content, 0o600); err != nil {
		return errs.Wrap(err, errs.ClassState,
			"check permissions on "+d.skillsDir, "writing skill %q", name)
	}
	return nil
}

// WriteResource writes one of a skill's supporting files beside its SKILL.md,
// reproducing the layout the skill was published with. That is what makes a
// relative reference in the body — references/api.md, scripts/extract.py —
// resolve the way it does for a skill placed there by hand.
func (d *dirAdapter) WriteResource(skill, relPath string, content []byte) error {
	dest, err := d.resourcePath(skill, relPath)
	if err != nil {
		return err
	}
	if err := atomicfs.WriteFile(dest, content, 0o600); err != nil {
		return errs.Wrap(err, errs.ClassState,
			"check permissions on "+d.skillsDir, "writing resource %q of skill %q", relPath, skill)
	}
	return nil
}

// resourcePath places a resource inside the skill's own directory, and refuses
// anything that would land outside it.
//
// The fetch layer already rejects path escapes, so reaching here with one means
// something upstream failed. This is the last point before bytes land in the
// user's tool directory, which makes it the wrong place to rely on a check made
// somewhere else.
func (d *dirAdapter) resourcePath(skill, relPath string) (string, error) {
	reject := func() (string, error) {
		return "", errs.Integrity(
			"do not install this skill; report the source",
			"skill %q ships a resource whose path escapes its directory: %q", skill, relPath)
	}
	if relPath == "" || strings.ContainsRune(relPath, 0) || path.IsAbs(relPath) ||
		filepath.IsAbs(relPath) || strings.Contains(relPath, "\\") {
		return reject()
	}

	dir := filepath.Dir(d.SkillPath(skill))
	dest := filepath.Join(dir, filepath.FromSlash(relPath))

	// Join cleans as it goes, so a traversal shows up as a destination that no
	// longer sits under the skill's directory.
	rel, err := filepath.Rel(dir, dest)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return reject()
	}
	return dest, nil
}

func (d *dirAdapter) RemoveSkill(name string) error {
	dir := filepath.Dir(d.SkillPath(name))
	if err := os.RemoveAll(dir); err != nil {
		return errs.Wrap(err, errs.ClassState,
			"check permissions on "+d.skillsDir, "removing skill %q", name)
	}
	return nil
}

func (d *dirAdapter) ReadSkill(name string) ([]byte, error) {
	b, err := os.ReadFile(d.SkillPath(name))
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
