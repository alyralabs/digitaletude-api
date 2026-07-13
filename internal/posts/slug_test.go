package posts

import (
	"regexp"
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := []struct {
		name  string
		title string
		want  string
	}{
		{"simple", "Hello World", "hello-world"},
		{"punctuation and casing", "C++ is Great!!!", "c-is-great"},
		{"leading/trailing junk", "  Leading/Trailing!! ", "leading-trailing"},
		{"already hyphenated", "Already-Hyphenated-Title", "already-hyphenated-title"},
		{"repeated whitespace", "Repeated   Spaces", "repeated-spaces"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := slugify(tc.title)
			if got != tc.want {
				t.Errorf("slugify(%q) = %q, want %q", tc.title, got, tc.want)
			}
		})
	}
}

func TestSlugifyFallback(t *testing.T) {
	// Nothing alphanumeric survives stripping, so slugify must fall back to
	// a short random id rather than returning an empty (unusable) slug.
	got := slugify("🎉🎊 --- !!!")
	if got == "" {
		t.Fatal("slugify returned empty string for an all-symbol title")
	}
	if !regexp.MustCompile(`^[0-9a-f]+$`).MatchString(got) {
		t.Errorf("fallback slug %q is not a hex id", got)
	}
}

func TestDeriveExcerpt(t *testing.T) {
	cases := []struct {
		name     string
		markdown string
		maxLen   int
		want     string
	}{
		{
			name:     "strips headings, emphasis, and links without truncating",
			markdown: "# Heading\n\nSome **bold** and _italic_ text with a [link](http://x.com).",
			maxLen:   200,
			want:     "Heading Some bold and italic text with a link.",
		},
		{
			name:     "under maxLen returns unchanged",
			markdown: "Brief",
			maxLen:   100,
			want:     "Brief",
		},
		{
			name:     "empty input returns empty",
			markdown: "",
			maxLen:   10,
			want:     "",
		},
		{
			name:     "truncates at a word boundary and adds ellipsis",
			markdown: "Short and then much more text after that continues on past the limit",
			maxLen:   5,
			want:     "Short…",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveExcerpt(tc.markdown, tc.maxLen)
			if got != tc.want {
				t.Errorf("deriveExcerpt(%q, %d) = %q, want %q", tc.markdown, tc.maxLen, got, tc.want)
			}
		})
	}
}

func TestDeriveExcerptStripsCodeFences(t *testing.T) {
	markdown := "Intro text.\n\n```go\nfmt.Println(\"hi\")\n```\n\nOutro text."
	got := deriveExcerpt(markdown, 200)
	if strings.Contains(got, "fmt.Println") {
		t.Errorf("deriveExcerpt did not strip code fence contents: %q", got)
	}
	want := "Intro text. Outro text."
	if got != want {
		t.Errorf("deriveExcerpt(code fence) = %q, want %q", got, want)
	}
}
