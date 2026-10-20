//go:build windows

package desktop

import (
	"strings"
	"testing"
	"unicode/utf16"
)

func TestNotificationUTF16Boundaries(t *testing.T) {
	for _, value := range []string{"日本語", strings.Repeat("😀", 100), strings.Repeat("長", 300)} {
		for _, capacity := range []int{2, 3, 64, 256} {
			got := boundedUTF16(value, capacity)
			if len(got) > capacity || got[len(got)-1] != 0 {
				t.Fatal("unterminated or oversized notification")
			}
			if strings.ContainsRune(string(utf16.Decode(got[:len(got)-1])), '\ufffd') {
				t.Fatal("surrogate pair split")
			}
		}
	}
}
