package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type candidate struct {
	profile     Profile
	filePath    string
	projectPath string
}

// Resolve finds the best-matching profile for the given name from the cwd context.
// If overridePath is non-empty, only that file is consulted (--config flag).
func Resolve(cwd, name, home, overridePath string) (Profile, string, error) {
	if overridePath != "" {
		return resolveFromFile(overridePath, name, cwd)
	}

	candidates := collectCandidates(cwd, name, home)

	if len(candidates) == 0 {
		return Profile{}, "", fmt.Errorf(
			"no profile named %q applies to directory %s; "+
				"run 'stackstart list' to see available profiles, or 'stackstart init' to create one",
			name, cwd)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return len(candidates[i].projectPath) > len(candidates[j].projectPath)
	})

	winner := candidates[0]
	winner.profile.ResolvedProjectPath = winner.projectPath
	return winner.profile, winner.filePath, nil
}

func resolveFromFile(path, name, cwd string) (Profile, string, error) {
	f, err := ParseFile(path)
	if err != nil {
		return Profile{}, "", err
	}
	profile, ok := f.Profiles[name]
	if !ok {
		available := make([]string, 0, len(f.Profiles))
		for k := range f.Profiles {
			available = append(available, k)
		}
		return Profile{}, "", fmt.Errorf(
			"profile %q not found in %s (available: %s)", name, path, strings.Join(available, ", "))
	}
	pp := profile.ProjectPath
	if pp == "" {
		pp = filepath.Dir(path)
	}
	profile.ResolvedProjectPath = pp
	return profile, path, nil
}

func collectCandidates(cwd, name, home string) []candidate {
	var candidates []candidate

	dir := cwd
	for {
		path := filepath.Join(dir, "stackstart.yaml")
		if f, err := ParseFile(path); err == nil {
			if p, ok := f.Profiles[name]; ok {
				pp := p.ProjectPath
				if pp == "" {
					pp = dir
				}
				if isWithinOrEqual(cwd, pp) {
					candidates = append(candidates, candidate{p, path, pp})
				}
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	userPath := filepath.Join(home, "stackstart.yaml")
	if f, err := ParseFile(userPath); err == nil {
		for pName, p := range f.Profiles {
			if pName == name && p.ProjectPath != "" && isWithinOrEqual(cwd, p.ProjectPath) {
				candidates = append(candidates, candidate{p, userPath, p.ProjectPath})
			}
		}
	}

	return candidates
}

// isWithinOrEqual returns true if child is within-or-equal to parent
// at directory boundaries.
func isWithinOrEqual(child, parent string) bool {
	child = filepath.Clean(child)
	parent = filepath.Clean(parent)
	if child == parent {
		return true
	}
	prefix := parent + string(os.PathSeparator)
	return strings.HasPrefix(child, prefix)
}

// ListProfiles returns all profiles visible from the given cwd context.
func ListProfiles(cwd, home, overridePath string) ([]ProfileEntry, error) {
	if overridePath != "" {
		return listFromFile(overridePath)
	}

	var entries []ProfileEntry
	seen := make(map[string]bool)

	dir := cwd
	for {
		path := filepath.Join(dir, "stackstart.yaml")
		if f, err := ParseFile(path); err == nil {
			for name := range f.Profiles {
				key := name + "|" + path
				if !seen[key] {
					seen[key] = true
					entries = append(entries, ProfileEntry{Name: name, FilePath: path})
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	userPath := filepath.Join(home, "stackstart.yaml")
	if f, err := ParseFile(userPath); err == nil {
		for name, p := range f.Profiles {
			if p.ProjectPath != "" && isWithinOrEqual(cwd, p.ProjectPath) {
				key := name + "|" + userPath
				if !seen[key] {
					seen[key] = true
					entries = append(entries, ProfileEntry{Name: name, FilePath: userPath})
				}
			}
		}
	}

	return entries, nil
}

func listFromFile(path string) ([]ProfileEntry, error) {
	f, err := ParseFile(path)
	if err != nil {
		return nil, err
	}
	var entries []ProfileEntry
	for name := range f.Profiles {
		entries = append(entries, ProfileEntry{Name: name, FilePath: path})
	}
	return entries, nil
}
