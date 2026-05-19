package cmd

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// safeFileSystem wraps http.Dir to block:
//   - paths containing dotfile components (e.g. "/.git/")
//   - symlinks that resolve outside the root directory
type safeFileSystem struct {
	root string
	fs   http.Dir
}

func newSafeFileSystem(root string) safeFileSystem {
	// Resolve to an absolute, symlink-evaluated path so prefix checks below work
	// even when the caller passed a relative path.
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return safeFileSystem{root: filepath.Clean(abs), fs: http.Dir(abs)}
}

func (sfs safeFileSystem) Open(name string) (http.File, error) {
	// Block any path component that starts with a dot.
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if strings.HasPrefix(part, ".") && part != "" && part != "." {
			return nil, os.ErrNotExist
		}
	}

	// Resolve the absolute path and ensure it stays within root.
	abs := filepath.Join(sfs.root, filepath.FromSlash(name))
	abs = filepath.Clean(abs)

	// Guard against symlinks pointing outside the root.
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// If the path doesn't exist yet that's fine; let http.Dir handle it.
		return sfs.fs.Open(name)
	}
	if !strings.HasPrefix(real, sfs.root+string(os.PathSeparator)) && real != sfs.root {
		return nil, os.ErrNotExist
	}

	return sfs.fs.Open(name)
}
