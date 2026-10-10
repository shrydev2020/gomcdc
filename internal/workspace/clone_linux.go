package workspace

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

func cloneRegularFile(source, destination string, mode fs.FileMode) (cloned bool, err error) {
	in, err := os.Open(source)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, in.Close()) }()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, err
	}
	if cloneErr := unix.IoctlFileClone(int(out.Fd()), int(in.Fd())); cloneErr != nil {
		return false, errors.Join(out.Close(), os.Remove(destination))
	}
	return true, errors.Join(out.Chmod(mode.Perm()), out.Close())
}
