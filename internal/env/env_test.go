package env

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadDotenv_Basic(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".env")
	require.NoError(t, os.WriteFile(path, []byte(`
# comment
KEY1=value1
KEY2="quoted value"
KEY3='single quoted'
EMPTY=

`), 0644))

	env, err := LoadDotenv(path)
	require.NoError(t, err)
	require.Equal(t, "value1", env["KEY1"])
	require.Equal(t, "quoted value", env["KEY2"])
	require.Equal(t, "single quoted", env["KEY3"])
	require.Equal(t, "", env["EMPTY"])
}

func TestLoadDotenv_NotFound(t *testing.T) {
	_, err := LoadDotenv("/nonexistent/.env")
	require.Error(t, err)
}

func TestLoadDotenv_InvalidLine(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".env")
	require.NoError(t, os.WriteFile(path, []byte("no_equals_sign"), 0644))

	_, err := LoadDotenv(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no '='")
}

func TestLoadNativeEnvrc_ValidExports(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".envrc")
	require.NoError(t, os.WriteFile(path, []byte(`
# comment
export DATABASE_URL=postgres://localhost/db
export SECRET="my secret"
export TOKEN='abc123'

`), 0644))

	env, err := loadNative(path)
	require.NoError(t, err)
	require.Equal(t, "postgres://localhost/db", env["DATABASE_URL"])
	require.Equal(t, "my secret", env["SECRET"])
	require.Equal(t, "abc123", env["TOKEN"])
}

func TestLoadNativeEnvrc_UnsupportedDirective(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".envrc")
	require.NoError(t, os.WriteFile(path, []byte(`source_up`), 0644))

	_, err := loadNative(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires direnv")
}

func TestLoadEnvrc_NoFile(t *testing.T) {
	env, err := LoadEnvrc(t.TempDir())
	require.NoError(t, err)
	require.Nil(t, env)
}
