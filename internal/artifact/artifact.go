// Package artifact defines the domain types that flow through the system.
//
// The fundamental unit is the set: a skill is a main document plus zero or
// more supporting files, each identified by a path relative to the skill's
// root.
package artifact

import "sort"

// DocumentKey is the canonical path of the main document inside the set.
// Resources never use this path.
const DocumentKey = "SKILL.md"

// Kind distinguishes the kinds of loadable artifact. v1 has only KindSkill;
// KindAgent is anticipated by the extension points and not implemented.
type Kind int

const (
	KindSkill Kind = iota
)

func (k Kind) String() string {
	switch k {
	case KindSkill:
		return "skill"
	default:
		return "unknown"
	}
}

// Origin records where the metadata came from, so derived values stay
// distinguishable from authored ones.
type Origin int

const (
	OriginFrontmatter Origin = iota
	OriginDerived
	OriginUserOverride
	OriginRegistry // reserved for U2
)

func (o Origin) String() string {
	switch o {
	case OriginFrontmatter:
		return "frontmatter"
	case OriginDerived:
		return "derived"
	case OriginUserOverride:
		return "set by user"
	case OriginRegistry:
		return "registry"
	default:
		return "unknown"
	}
}

// Metadata carries a skill's short name and description.
type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Origin      Origin `json:"origin"`
}

// Resource is a supporting file that accompanies the main document.
type Resource struct {
	RelPath string `json:"relPath"`
	Content []byte `json:"-"`
}

// Artifact is the complete set, resolved in memory.
type Artifact struct {
	Kind      Kind
	Document  []byte
	Resources []Resource
	Meta      Metadata
	Version   string
}

// Normalize sorts resources canonically by relative path. The ordering is a
// precondition for the set hash not depending on the order files happened to
// be read in.
func (a *Artifact) Normalize() {
	sort.Slice(a.Resources, func(i, j int) bool {
		return a.Resources[i].RelPath < a.Resources[j].RelPath
	})
}

// TotalResourceSize returns the combined size of the supporting files.
func (a *Artifact) TotalResourceSize() int {
	n := 0
	for _, r := range a.Resources {
		n += len(r.Content)
	}
	return n
}

// IndexEntry is the lightweight record returned by a registry search. Reserved
// for U2; declared here because the type belongs to the domain.
type IndexEntry struct {
	ID       string `json:"id"`
	SkillID  string `json:"skillId"`
	Name     string `json:"name"`
	Source   string `json:"source"`
	Installs int    `json:"installs"`
}
