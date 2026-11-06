package managed

import _ "embed"

// Instructions are compiled into the same executable that implements them.
//
//go:embed instructions.md
var Instructions string
