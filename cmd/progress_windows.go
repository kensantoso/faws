package cmd

import "io"

// terminalSize disables the live block on Windows, where ANSI support varies.
func terminalSize(io.Writer) (cols, rows int, ok bool) { return 0, 0, false }
