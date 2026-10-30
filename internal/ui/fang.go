package ui

import (
	"charm.land/fang/v2"
	"charm.land/lipgloss/v2"
)

// FangColors themes cobra help and errors with the brand palette.
func FangColors(c lipgloss.LightDarkFunc) fang.ColorScheme {
	s := fang.DefaultColorScheme(c)
	s.Title = c(lipgloss.Color("#5B47D6"), lipgloss.Color("#A99BFF"))
	s.Command = c(lipgloss.Color("#5B47D6"), lipgloss.Color("#C9C0FF"))
	s.Program = s.Command
	s.Flag = c(lipgloss.Color("#1E8A55"), lipgloss.Color("#5FD39A"))
	s.QuotedString = c(lipgloss.Color("#9A7B00"), lipgloss.Color("#E8C84A"))
	return s
}
