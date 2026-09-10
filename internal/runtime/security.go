package runtime

import (
	"path/filepath"
)

// IsDestructiveCommand recognizes only high-confidence destructive commands.
func IsDestructiveCommand(executable string, args []string) bool {
	switch filepath.Base(executable) {
	case "rm", "rmdir", "unlink":
		return true
	case "git":
		return len(args) > 0 && args[0] == "clean"
	case "find":
		for _, arg := range args {
			if arg == "-delete" {
				return true
			}
		}
	}
	return false
}
