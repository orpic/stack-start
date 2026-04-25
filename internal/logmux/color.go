package logmux

import (
	"hash/fnv"

	"github.com/fatih/color"
)

var palette = []color.Attribute{
	color.FgCyan, color.FgGreen, color.FgYellow, color.FgMagenta,
	color.FgBlue, color.FgRed, color.FgHiCyan, color.FgHiGreen,
}

// ColorFor returns a stable color for the given process name.
func ColorFor(name string) color.Attribute {
	h := fnv.New32a()
	h.Write([]byte(name))
	return palette[int(h.Sum32())%len(palette)]
}
