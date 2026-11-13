package checks

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// matchSegments reports whether path segments match pattern segments, where
// "**" matches zero or more whole segments and other segments use
// filepath.Match semantics (*, ?, [...]).
func matchSegments(pat, name []string) bool {
	if len(pat) == 0 {
		return len(name) == 0
	}
	if pat[0] == "**" {
		// ** matches zero or more segments.
		for i := 0; i <= len(name); i++ {
			if matchSegments(pat[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	ok, err := filepath.Match(pat[0], name[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pat[1:], name[1:])
}

// MatchPath reports whether a slash-separated relative path matches a glob
// pattern that may contain "**".
func MatchPath(pattern, path string) bool {
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)
	return matchSegments(strings.Split(pattern, "/"), strings.Split(path, "/"))
}

// GlobFiles walks root and returns the relative paths of regular files that
// match any of the patterns. Patterns are relative to root and may use "**".
// It returns files only (not directories), sorted for determinism.
func GlobFiles(root string, patterns []string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// Skip unreadable entries rather than aborting the whole walk.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		for _, pat := range patterns {
			if MatchPath(pat, rel) {
				out = append(out, filepath.ToSlash(rel))
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}