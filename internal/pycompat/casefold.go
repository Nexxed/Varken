package pycompat

import (
	"sort"
	"strings"
)

type runeRange struct{ lo, hi rune }

func inRanges(table []runeRange, r rune) bool {
	i := sort.Search(len(table), func(i int) bool { return table[i].hi >= r })
	return i < len(table) && table[i].lo <= r
}

func isCased(r rune) bool         { return inRanges(casedRanges, r) }
func isCaseIgnorable(r rune) bool { return inRanges(caseIgnorableRanges, r) }

func mapRune(table map[rune]string, b *strings.Builder, r rune) {
	if m, ok := table[r]; ok {
		b.WriteString(m)
		return
	}
	b.WriteRune(r)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// Upper is Python's str.upper(), including full mappings such as ß -> SS.
func Upper(s string) string {
	if isASCII(s) {
		return strings.ToUpper(s)
	}
	var b strings.Builder
	for _, r := range s {
		mapRune(upperFull, &b, r)
	}
	return b.String()
}

// Lower is Python's str.lower(), including the Final_Sigma rule for Σ.
func Lower(s string) string {
	if isASCII(s) {
		return strings.ToLower(s)
	}
	runes := []rune(s)
	var b strings.Builder
	for i, r := range runes {
		if r == 0x3a3 {
			b.WriteRune(sigma(runes, i))
			continue
		}
		mapRune(lowerFull, &b, r)
	}
	return b.String()
}

// Title is Python's str.title(): a character is title-cased when the one before it is not cased, and lower-cased
// otherwise. So "they're" becomes "They'Re", and "4k" becomes "4K".
func Title(s string) string {
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	previousCased := false
	for i, r := range runes {
		switch {
		case !previousCased:
			mapRune(titleFull, &b, r)
		case r == 0x3a3:
			b.WriteRune(sigma(runes, i))
		default:
			mapRune(lowerFull, &b, r)
		}
		previousCased = isCased(r)
	}
	return b.String()
}

// sigma picks the lowercase of the capital sigma at runes[i]: final (ς) when it ends a word, σ otherwise.
func sigma(runes []rune, i int) rune {
	j := i - 1
	for j >= 0 && isCaseIgnorable(runes[j]) {
		j--
	}
	final := j >= 0 && isCased(runes[j])
	if final && i+1 < len(runes) {
		k := i + 1
		for k < len(runes) && isCaseIgnorable(runes[k]) {
			k++
		}
		final = k == len(runes) || !isCased(runes[k])
	}
	if final {
		return 0x3c2
	}
	return 0x3c3
}
