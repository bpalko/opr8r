package terraform

import (
	"os"
)

// readFile is a helper function to read file contents
func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
