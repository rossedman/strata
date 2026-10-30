// Package ui holds every terminal style strata uses, built from the brand
// palette with adaptive colors that read on light and dark terminals, and
// the slog setup. Commands import styles from here; nothing defines colors
// inline.
//
// Machine output (json, csv, ndjson, mermaid, dot, svg, html) is never
// styled; only table and tree output go through these styles, and colors
// are dropped automatically when stdout is not a terminal or NO_COLOR is
// set.
package ui

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"charm.land/log/v2"
	"github.com/charmbracelet/colorprofile"
	"golang.org/x/term"
)

// Brand palette. Violet is the one color that means strata; the verdict
// colors each have one job.
var (
	Brand     = compat.AdaptiveColor{Light: lipgloss.Color("#5B47D6"), Dark: lipgloss.Color("#A99BFF")}
	Muted     = compat.AdaptiveColor{Light: lipgloss.Color("#6B6880"), Dark: lipgloss.Color("#8E8AA6")}
	Text      = compat.AdaptiveColor{Light: lipgloss.Color("#1D1A2E"), Dark: lipgloss.Color("#E9E6F5")}
	Fresh     = compat.AdaptiveColor{Light: lipgloss.Color("#1E8A55"), Dark: lipgloss.Color("#5FD39A")}
	Drifting  = compat.AdaptiveColor{Light: lipgloss.Color("#9A7B00"), Dark: lipgloss.Color("#E8C84A")}
	Stale     = compat.AdaptiveColor{Light: lipgloss.Color("#C2451B"), Dark: lipgloss.Color("#F08A5D")}
	Abandoned = compat.AdaptiveColor{Light: lipgloss.Color("#6E6E78"), Dark: lipgloss.Color("#A3A3AD")}
	Conflict  = compat.AdaptiveColor{Light: lipgloss.Color("#C8243B"), Dark: lipgloss.Color("#FF6B81")}
)

// Styles used across commands.
var (
	Title   = lipgloss.NewStyle().Bold(true).Foreground(Brand)
	Header  = lipgloss.NewStyle().Bold(true).Foreground(Muted)
	Label   = lipgloss.NewStyle().Foreground(Text)
	Dim     = lipgloss.NewStyle().Foreground(Muted)
	Key     = lipgloss.NewStyle().Foreground(Brand)
	Branch  = lipgloss.NewStyle().Foreground(Muted)
	Warning = lipgloss.NewStyle().Foreground(Stale)
	Bad     = lipgloss.NewStyle().Foreground(Conflict)
)

// VerdictStyle returns the style for a staleness verdict.
func VerdictStyle(verdict string) lipgloss.Style {
	c := map[string]compat.AdaptiveColor{"fresh": Fresh, "drifting": Drifting, "stale": Stale, "abandoned": Abandoned}[verdict]
	if c.Light == nil {
		return Dim
	}
	return lipgloss.NewStyle().Foreground(c).Bold(verdict != "fresh")
}

// Fprint writes styled text, downsampling colors to what w supports and
// dropping them for pipes, files and NO_COLOR.
func Fprint(w io.Writer, s string) {
	_, _ = lipgloss.Fprint(w, s)
}

// Profile returns the color profile for w.
func Profile(w io.Writer) colorprofile.Profile {
	return colorprofile.Detect(w, os.Environ())
}

// IsTerminal reports whether f is an interactive terminal. Character
// devices that are not terminals, such as /dev/null, are not.
func IsTerminal(f any) bool {
	file, ok := f.(*os.File)
	return ok && term.IsTerminal(int(file.Fd())) //nolint:gosec // file descriptors fit in an int
}

// Bytes formats a byte count with binary units.
func Bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// NewLogger returns a slog logger that writes human-friendly logs through
// charmbracelet/log to w. level is debug, info, warn or error.
func NewLogger(w io.Writer, level string) (*slog.Logger, error) {
	lvl, err := log.ParseLevel(level)
	if err != nil {
		return nil, fmt.Errorf("log level %q: %w", level, err)
	}
	l := log.NewWithOptions(w, log.Options{Level: lvl, ReportTimestamp: true, TimeFormat: "15:04:05"})
	l.SetColorProfile(Profile(w))
	styles := log.DefaultStyles()
	styles.Levels[log.InfoLevel] = styles.Levels[log.InfoLevel].Foreground(Brand)
	l.SetStyles(styles)
	return slog.New(l), nil
}
