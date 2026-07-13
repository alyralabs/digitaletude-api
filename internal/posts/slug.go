package posts

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
)

var (
	nonAlnumRun   = regexp.MustCompile(`[^a-z0-9]+`)
	mdCodeFence   = regexp.MustCompile("(?s)```.*?```")
	mdImage       = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	mdLink        = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	mdHeading     = regexp.MustCompile(`(?m)^#{1,6}\s*`)
	mdEmphasis    = regexp.MustCompile("[*_`]+")
	whitespaceRun = regexp.MustCompile(`\s+`)
)

// slugify lowercases the title and collapses runs of non-alphanumeric
// characters into a single hyphen, trimming the edges. A title with
// nothing alphanumeric in it (all-emoji, etc.) falls back to a short
// random id so every post still gets a usable URL segment.
func slugify(title string) string {
	s := nonAlnumRun.ReplaceAllString(strings.ToLower(title), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return randomID()
	}
	return s
}

func randomID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// deriveExcerpt strips common markdown syntax from content and truncates
// it to roughly maxLen runes at a word boundary. Used by the public post
// list when an author hasn't written an explicit excerpt.
func deriveExcerpt(markdown string, maxLen int) string {
	s := mdCodeFence.ReplaceAllString(markdown, "")
	s = mdImage.ReplaceAllString(s, "")
	s = mdLink.ReplaceAllString(s, "$1")
	s = mdHeading.ReplaceAllString(s, "")
	s = mdEmphasis.ReplaceAllString(s, "")
	s = whitespaceRun.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)

	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	cut := string(runes[:maxLen])
	if i := strings.LastIndexByte(cut, ' '); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut) + "…"
}
