package ref_test

import (
	"strings"

	"pgregory.net/rapid"
)

// Domain generators: they produce realistic, structured references
// rather than random strings. A generator emitting loose text would exercise
// only the rejection path and never find a real bug.

var (
	genSegment = rapid.StringMatching(`[a-z][a-z0-9_.-]{0,20}`)
	genGitRef  = rapid.StringMatching(`[a-zA-Z][a-zA-Z0-9_.-]{0,15}(/[a-zA-Z][a-zA-Z0-9_.-]{0,10})?`)
	genHost    = rapid.StringMatching(`[a-z][a-z0-9-]{0,15}\.(com|org|dev|io)`)
)

// genGitHubRaw produces the textual form of a valid GitHub reference.
func genGitHubRaw(t *rapid.T) string {
	owner := genSegment.Draw(t, "owner")
	repo := genSegment.Draw(t, "repo")
	depth := rapid.IntRange(1, 4).Draw(t, "depth")
	segs := make([]string, 0, depth)
	for i := 0; i < depth; i++ {
		segs = append(segs, genSegment.Draw(t, "pathSeg"))
	}
	if rapid.Bool().Draw(t, "isDoc") {
		segs[len(segs)-1] += ".md"
	}
	s := "gh:" + owner + "/" + repo + "/" + strings.Join(segs, "/")
	if rapid.Bool().Draw(t, "hasGitRef") {
		s += "@" + genGitRef.Draw(t, "gitRef")
	}
	return s
}

// genURLRaw produces the textual form of a valid URL reference.
func genURLRaw(t *rapid.T) string {
	host := genHost.Draw(t, "host")
	depth := rapid.IntRange(1, 3).Draw(t, "depth")
	segs := make([]string, 0, depth)
	for i := 0; i < depth; i++ {
		segs = append(segs, genSegment.Draw(t, "urlSeg"))
	}
	return "https://" + host + "/" + strings.Join(segs, "/") + ".md"
}

// genAnyRaw produces any reference valid in U1.
func genAnyRaw(t *rapid.T) string {
	if rapid.Bool().Draw(t, "isGitHub") {
		return genGitHubRaw(t)
	}
	return genURLRaw(t)
}
