//go:build screen && !windows

package screen

import "os"

func rootEntries() []fsEntry { return []fsEntry{{Name: "/", Dir: true}} }

func userHomeDir() string {
	h, _ := os.UserHomeDir()
	return h
}
