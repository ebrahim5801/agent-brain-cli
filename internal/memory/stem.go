package memory

import "github.com/kljensen/snowball/english"

// stem reduces a lowercase token to its English word stem (Porter2/Snowball),
// so morphological variants like run/running collapse to the same term for
// both corpus and query tokens. stemStopWords=true avoids depending on the
// library's stop-word list for search purposes.
func stem(word string) string {
	return english.Stem(word, true)
}
