package logmux

import "fmt"

// FormatPrefix returns a left-padded "name | " prefix string.
func FormatPrefix(name string, padTo int) string {
	return fmt.Sprintf("%-*s | ", padTo, name)
}
