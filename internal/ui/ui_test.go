package ui

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestBytes(t *testing.T) {
	t.Parallel()
	tests := map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 5 << 20: "5.0 MiB", 3 << 40: "3.0 TiB"}
	for in, want := range tests {
		if got := Bytes(in); got != want {
			t.Errorf("Bytes(%d) = %s, want %s", in, got, want)
		}
	}
}

func TestStyles(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"fresh", "drifting", "stale", "abandoned", ""} {
		if VerdictStyle(v).Render(v+"x") == "" {
			t.Errorf("VerdictStyle(%q) renders nothing", v)
		}
	}
	var b bytes.Buffer
	Fprint(&b, Title.Render("hi"))
	if !strings.Contains(b.String(), "hi") || strings.Contains(b.String(), "\x1b[") {
		t.Fatalf("Fprint to a buffer kept escape codes: %q", b.String())
	}
	s := FangColors(lipgloss.LightDark(true))
	if s.Title == nil || s.Flag == nil {
		t.Fatal("fang colors unset")
	}
}

func TestTerminalAndLogger(t *testing.T) {
	t.Parallel()
	if IsTerminal(&bytes.Buffer{}) {
		t.Fatal("buffer is not a terminal")
	}
	f, err := os.CreateTemp(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if IsTerminal(f) {
		t.Fatal("file is not a terminal")
	}
	var b bytes.Buffer
	l, err := NewLogger(&b, "debug")
	if err != nil {
		t.Fatal(err)
	}
	l.Info("indexed", "repos", 3)
	if !strings.Contains(b.String(), "indexed") || !strings.Contains(b.String(), "repos=3") {
		t.Fatalf("log output = %q", b.String())
	}
	if _, err := NewLogger(&b, "loud"); err == nil {
		t.Fatal("bad level accepted")
	}
	if Profile(&b).String() == "" {
		t.Fatal("no profile")
	}
}
