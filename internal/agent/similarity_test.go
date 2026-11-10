package agent

import "testing"

func TestNormalizeObservation(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "edges", input: "  hello  ", want: "hello"},
		{name: "whitespace", input: "a\r\n b\t\tc", want: "a b c"},
		{name: "newlines", input: "line1\rline2\nline3", want: "line1 line2 line3"},
		{name: "empty", input: " \r\n\t ", want: ""},
		{name: "preserve case and punctuation", input: "Hello, 世界!", want: "Hello, 世界!"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeObservation(tt.input); got != tt.want {
				t.Fatalf("normalizeObservation() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestObservationSimilarity(t *testing.T) {
	if got := observationSimilarity("hello world", "  hello   world "); got != 1 {
		t.Fatalf("similarity(identical) = %v, want 1", got)
	}
	if got := observationSimilarity("你", "你"); got != 1 {
		t.Fatalf("similarity(short identical) = %v, want 1", got)
	}
	if got := observationSimilarity("你", "我"); got != 0 {
		t.Fatalf("similarity(short different) = %v, want 0", got)
	}
	if got := observationSimilarity("abcdef", "uvwxyz"); got != 0 {
		t.Fatalf("similarity(disjoint) = %v, want 0", got)
	}
	similar := observationSimilarity("hello world", "hello there")
	if similar <= 0 || similar >= 1 {
		t.Fatalf("similarity(similar) = %v, want in (0,1)", similar)
	}
	chinese := observationSimilarity("你好世界", "你好朋友")
	if chinese <= 0 || chinese >= 1 {
		t.Fatalf("similarity(chinese) = %v, want in (0,1)", chinese)
	}
}

func TestIsNoProgress(t *testing.T) {
	tests := []struct {
		name      string
		prev      string
		current   string
		threshold float64
		minChars  int
		want      bool
	}{
		{name: "disabled", prev: "same", current: "same", threshold: 0, minChars: 80, want: false},
		{name: "short equal", prev: "ok", current: "ok", threshold: 0.95, minChars: 80, want: true},
		{name: "short different", prev: "ok", current: "done", threshold: 0.95, minChars: 80, want: false},
		{name: "short equal with min zero", prev: "x", current: "x", threshold: 1, minChars: 0, want: true},
		{name: "short different with min zero", prev: "x", current: "y", threshold: 1, minChars: 0, want: false},
		{name: "long identical", prev: "hello world", current: "hello world", threshold: 1, minChars: 0, want: true},
		{name: "long similar", prev: "hello world", current: "hello worlds", threshold: 0.5, minChars: 0, want: true},
		{name: "long different", prev: "abcdef", current: "uvwxyz", threshold: 0.9, minChars: 0, want: false},
		{name: "long below threshold", prev: "hello world", current: "hello worlds", threshold: 1, minChars: 0, want: false},
		{name: "normalized equal", prev: "a\r\n b", current: "a b", threshold: 0.95, minChars: 80, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNoProgress(tt.prev, tt.current, tt.threshold, tt.minChars); got != tt.want {
				t.Fatalf("isNoProgress() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBigramSimilarityDirect(t *testing.T) {
	if got := bigramSimilarity("abc", "abc"); got != 1 {
		t.Fatalf("bigramSimilarity(equal) = %v", got)
	}
	if got := bigramSimilarity("a", "a"); got != 1 {
		t.Fatalf("bigramSimilarity(one rune equal) = %v", got)
	}
	if got := bigramSimilarity("a", "b"); got != 0 {
		t.Fatalf("bigramSimilarity(one rune different) = %v", got)
	}
}
