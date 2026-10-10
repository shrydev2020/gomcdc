//go:build !darwin && !linux

package workspace

import "io/fs"

func cloneRegularFile(_, _ string, _ fs.FileMode) (bool, error) { return false, nil }
