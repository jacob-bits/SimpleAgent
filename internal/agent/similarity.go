package agent

import (
	"strings"
	"unicode/utf8"
)

// normalizeObservation trims edges and collapses every run of Unicode
// whitespace to one space. The result preserves order and case.
func normalizeObservation(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.Join(strings.Fields(value), " ")
}

// observationSimilarity returns the Unicode character-bigram Jaccard
// similarity of two normalized observation bodies.
func observationSimilarity(a, b string) float64 {
	return bigramSimilarity(normalizeObservation(a), normalizeObservation(b))
}

// isNoProgress applies the threshold and the short-text exact-equality rule.
func isNoProgress(prev, current string, threshold float64, minChars int) bool {
	if threshold <= 0 {
		return false
	}

	a := normalizeObservation(prev)
	b := normalizeObservation(current)
	if a == b {
		return true
	}
	if utf8.RuneCountInString(a) < minChars || utf8.RuneCountInString(b) < minChars {
		return false
	}
	return bigramSimilarity(a, b) >= threshold
}

func bigramSimilarity(a, b string) float64 {
	if a == b {
		return 1
	}

	ar := []rune(a)
	br := []rune(b)
	if len(ar) < 2 || len(br) < 2 {
		return 0
	}

	left := bigramSet(ar)
	right := bigramSet(br)
	intersection := 0
	for pair := range left {
		if _, ok := right[pair]; ok {
			intersection++
		}
	}
	union := len(left) + len(right) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func bigramSet(runes []rune) map[[2]rune]struct{} {
	set := make(map[[2]rune]struct{}, len(runes)-1)
	for i := 0; i < len(runes)-1; i++ {
		set[[2]rune{runes[i], runes[i+1]}] = struct{}{}
	}
	return set
}
