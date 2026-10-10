package workspace

import (
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// Clonefile creates an independent inode backed by copy-on-write extents.
// Unsupported filesystems and cross-device copies use the streaming path.
func cloneRegularFile(source, destination string, mode fs.FileMode) (bool, error) {
	if err := unix.Clonefile(source, destination, unix.CLONE_NOFOLLOW); err != nil {
		return false, nil
	}
	return true, os.Chmod(destination, mode.Perm())
}
