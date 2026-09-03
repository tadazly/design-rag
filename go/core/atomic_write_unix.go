//go:build !windows

package core

import "os"

func replaceFileAtomic(source, destination string) error {
	return os.Rename(source, destination)
}
