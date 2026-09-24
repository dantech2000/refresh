package nodegroup

import (
	"fmt"
	"strings"
	"testing"
)

// FuzzReleaseDate checks releaseDate on any string (an accepted date is an
// 8-digit substring of the input) and on well-formed amazon-eks-ami release
// versions and tags, where it must return the date stamp and so order
// releases by date.
func FuzzReleaseDate(f *testing.F) {
	f.Add("1.31.0-20260601", uint16(31), uint16(0), uint32(20260601), uint32(20260615))
	f.Add("v20260601", uint16(33), uint16(4), uint32(20251231), uint32(20260101))
	f.Add("1.31.0-2026060", uint16(9), uint16(65535), uint32(0), uint32(99999999))
	f.Add("", uint16(0), uint16(0), uint32(19700101), uint32(19700101))
	f.Add("1.30.1-202606011234", uint16(30), uint16(1), uint32(12345678), uint32(12345679))
	f.Fuzz(func(t *testing.T, raw string, minor, patch uint16, d1, d2 uint32) {
		if got, ok := releaseDate(raw); ok {
			if len(got) != 8 || strings.TrimLeft(got, "0123456789") != "" || !strings.Contains(raw, got) {
				t.Fatalf("releaseDate(%q) = %q; want an 8-digit substring of the input", raw, got)
			}
		} else if got != "" {
			t.Fatalf("releaseDate(%q) = %q, false; want empty when not ok", raw, got)
		}

		d1, d2 = d1%100000000, d2%100000000
		stamp1, stamp2 := fmt.Sprintf("%08d", d1), fmt.Sprintf("%08d", d2)
		for _, s := range []string{
			fmt.Sprintf("1.%d.%d-%s", minor, patch, stamp1),
			"v" + stamp1,
		} {
			if got, ok := releaseDate(s); !ok || got != stamp1 {
				t.Fatalf("releaseDate(%q) = %q, %v; want %q", s, got, ok, stamp1)
			}
		}
		r1, _ := releaseDate(fmt.Sprintf("1.%d.%d-%s", minor, patch, stamp1))
		r2, _ := releaseDate("v" + stamp2)
		if (r1 < r2) != (d1 < d2) || (r1 == r2) != (d1 == d2) {
			t.Fatalf("releaseDate order %q vs %q disagrees with dates %d vs %d", r1, r2, d1, d2)
		}
	})
}
