package chain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// digest returns a valid sha256 digest derived from s, for test fixtures.
func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestIDs(t *testing.T) {
	t.Parallel()
	a, b, c := digest("a"), digest("b"), digest("c")
	ab := sha256.Sum256([]byte(a + " " + b))
	abHex := "sha256:" + hex.EncodeToString(ab[:])
	abc := sha256.Sum256([]byte(abHex + " " + c))
	abcHex := "sha256:" + hex.EncodeToString(abc[:])

	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, []string{}},
		{"one layer is its own chain", []string{a}, []string{a}},
		{"two layers", []string{a, b}, []string{a, abHex}},
		{"three layers", []string{a, b, c}, []string{a, abHex, abcHex}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := IDs(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("IDs = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTop(t *testing.T) {
	t.Parallel()
	if top, err := Top(nil); err != nil || top != "" {
		t.Fatalf("Top(nil) = %q, %v", top, err)
	}
	a, b := digest("a"), digest("b")
	ids, _ := IDs([]string{a, b})
	if top, _ := Top([]string{a, b}); top != ids[1] {
		t.Fatalf("Top = %s, want %s", top, ids[1])
	}
	if _, err := Top([]string{"nope"}); err == nil {
		t.Fatal("Top accepted an invalid diff ID")
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in string
		ok bool
	}{
		{digest("x"), true},
		{"sha512:" + strings.Repeat("a", 64), false},
		{"sha256:" + strings.Repeat("a", 63), false},
		{"sha256:" + strings.Repeat("A", 64), false},
		{"sha256:" + strings.Repeat("g", 64), false},
		{"", false},
	}
	for _, tt := range tests {
		if err := Validate(tt.in); (err == nil) != tt.ok {
			t.Errorf("Validate(%q) = %v, want ok=%v", tt.in, err, tt.ok)
		}
	}
	if _, err := IDs([]string{digest("a"), "bad"}); err == nil {
		t.Fatal("IDs accepted an invalid diff ID")
	}
}

func BenchmarkIDs(b *testing.B) {
	layers := make([]string, 30)
	for i := range layers {
		layers[i] = digest(strings.Repeat("l", i+1))
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := IDs(layers); err != nil {
			b.Fatal(err)
		}
	}
}

func FuzzIDs(f *testing.F) {
	f.Add(digest("a"), digest("b"), digest("c"))
	f.Add("sha256:", "", "x")
	f.Fuzz(func(t *testing.T, x, y, z string) {
		in := []string{x, y, z}
		ids, err := IDs(in)
		if err != nil {
			return
		}
		// Every chain ID is a valid digest, the first equals the first diff
		// ID, and the result of a prefix is a prefix of the result.
		for _, id := range ids {
			if Validate(id) != nil {
				t.Fatalf("invalid chain ID %q", id)
			}
		}
		if ids[0] != x {
			t.Fatalf("chain(1) = %q, want %q", ids[0], x)
		}
		short, err := IDs(in[:2])
		if err != nil || short[1] != ids[1] {
			t.Fatalf("prefix chain mismatch: %v %v", short, err)
		}
	})
}
