package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeYAML(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "stackstart.yaml")
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

func TestResolve_ProjectLocal(t *testing.T) {
	tmp := t.TempDir()
	writeYAML(t, tmp, `
profiles:
  dev:
    processes:
      app:
        cwd: .
        cmd: echo hi
`)

	profile, filePath, err := Resolve(tmp, "dev", t.TempDir(), "")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(tmp, "stackstart.yaml"), filePath)
	require.Equal(t, tmp, profile.ResolvedProjectPath)
	require.Contains(t, profile.Processes, "app")
}

func TestResolve_UpwardWalk(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "packages", "backend")
	require.NoError(t, os.MkdirAll(child, 0755))

	writeYAML(t, root, `
profiles:
  dev:
    processes:
      app:
        cwd: .
        cmd: echo hi
`)

	profile, filePath, err := Resolve(child, "dev", t.TempDir(), "")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "stackstart.yaml"), filePath)
	require.Equal(t, root, profile.ResolvedProjectPath)
}

func TestResolve_UserLevel(t *testing.T) {
	home := t.TempDir()
	projectDir := t.TempDir()

	writeYAML(t, home, `
profiles:
  dev:
    project_path: `+projectDir+`
    processes:
      app:
        cwd: .
        cmd: echo hi
`)

	profile, filePath, err := Resolve(projectDir, "dev", home, "")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, "stackstart.yaml"), filePath)
	require.Equal(t, projectDir, profile.ResolvedProjectPath)
}

func TestResolve_LongestPathWins(t *testing.T) {
	root := t.TempDir()
	inner := filepath.Join(root, "packages", "api")
	require.NoError(t, os.MkdirAll(inner, 0755))

	writeYAML(t, root, `
profiles:
  dev:
    processes:
      outer:
        cwd: .
        cmd: echo outer
`)
	writeYAML(t, inner, `
profiles:
  dev:
    processes:
      inner:
        cwd: .
        cmd: echo inner
`)

	profile, _, err := Resolve(inner, "dev", t.TempDir(), "")
	require.NoError(t, err)
	require.Contains(t, profile.Processes, "inner")
	require.NotContains(t, profile.Processes, "outer")
}

func TestResolve_NoMatch(t *testing.T) {
	_, _, err := Resolve(t.TempDir(), "dev", t.TempDir(), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no profile named")
}

func TestResolve_OverrideConfig(t *testing.T) {
	tmp := t.TempDir()
	path := writeYAML(t, tmp, `
profiles:
  custom:
    processes:
      app:
        cwd: .
        cmd: echo custom
`)

	profile, filePath, err := Resolve("/some/random/dir", "custom", t.TempDir(), path)
	require.NoError(t, err)
	require.Equal(t, path, filePath)
	require.Contains(t, profile.Processes, "app")
}

func TestResolve_OverrideConfig_ProfileNotFound(t *testing.T) {
	tmp := t.TempDir()
	path := writeYAML(t, tmp, `
profiles:
  other:
    processes:
      app:
        cwd: .
        cmd: echo hi
`)

	_, _, err := Resolve(t.TempDir(), "dev", t.TempDir(), path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
	require.Contains(t, err.Error(), "other")
}

func TestResolve_UserLevel_WrongPath(t *testing.T) {
	home := t.TempDir()
	writeYAML(t, home, `
profiles:
  dev:
    project_path: /some/other/project
    processes:
      app:
        cwd: .
        cmd: echo hi
`)

	_, _, err := Resolve(t.TempDir(), "dev", home, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no profile named")
}

func TestIsWithinOrEqual(t *testing.T) {
	require.True(t, isWithinOrEqual("/a/b/c", "/a/b/c"))
	require.True(t, isWithinOrEqual("/a/b/c/d", "/a/b/c"))
	require.False(t, isWithinOrEqual("/a/b/cx", "/a/b/c"))
	require.False(t, isWithinOrEqual("/a/b", "/a/b/c"))
	require.True(t, isWithinOrEqual("/a", "/a"))
}

func TestListProfiles_Local(t *testing.T) {
	tmp := t.TempDir()
	writeYAML(t, tmp, `
profiles:
  dev:
    processes:
      app:
        cwd: .
        cmd: echo dev
  staging:
    processes:
      app:
        cwd: .
        cmd: echo staging
`)

	entries, err := ListProfiles(tmp, t.TempDir(), "")
	require.NoError(t, err)
	require.Len(t, entries, 2)

	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
	}
	require.True(t, names["dev"])
	require.True(t, names["staging"])
}

func TestListProfiles_Empty(t *testing.T) {
	entries, err := ListProfiles(t.TempDir(), t.TempDir(), "")
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestListProfiles_Override(t *testing.T) {
	tmp := t.TempDir()
	path := writeYAML(t, tmp, `
profiles:
  custom:
    processes:
      app:
        cwd: .
        cmd: echo hi
`)

	entries, err := ListProfiles(t.TempDir(), t.TempDir(), path)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "custom", entries[0].Name)
}
