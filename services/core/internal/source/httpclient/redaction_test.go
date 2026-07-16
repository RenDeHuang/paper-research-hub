package httpclient

import (
	"slices"
	"testing"
)

func TestNormalizeSecretsDropsEmptyDeduplicatesAndStableSortsLongestFirst(t *testing.T) {
	t.Parallel()

	got := normalizeSecrets([]string{
		"abc",
		"",
		"abcdef",
		"abc",
		"uvwxyz",
		"   ",
	})
	want := []string{"abcdef", "uvwxyz", "abc"}
	if !slices.Equal(got, want) {
		t.Fatalf("normalizeSecrets() = %#v, want %#v", got, want)
	}
}
