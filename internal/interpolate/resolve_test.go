package interpolate

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolve_Simple(t *testing.T) {
	data := RefData{"cloudflared.url": "https://example.trycloudflare.com"}
	result, err := Resolve("${cloudflared.url}", data)
	require.NoError(t, err)
	require.Equal(t, "https://example.trycloudflare.com", result)
}

func TestResolve_Mixed(t *testing.T) {
	result, err := Resolve("--tunnel=${cloudflared.url} --db=${envrc.DB}", RefData{
		"cloudflared.url": "https://tunnel.com",
		"envrc.DB":        "postgres://localhost",
	})
	require.NoError(t, err)
	require.Equal(t, "--tunnel=https://tunnel.com --db=postgres://localhost", result)
}

func TestResolve_Escape(t *testing.T) {
	result, err := Resolve("$$50", RefData{})
	require.NoError(t, err)
	require.Equal(t, "$50", result)
}

func TestResolve_MissingRef(t *testing.T) {
	_, err := Resolve("${missing.ref}", RefData{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unresolved reference")
}

func TestResolve_NoRefs(t *testing.T) {
	result, err := Resolve("plain text", RefData{})
	require.NoError(t, err)
	require.Equal(t, "plain text", result)
}

func TestBuildRefData(t *testing.T) {
	captures := map[string]map[string]string{
		"tunnel": {"url": "https://example.com"},
	}
	envrc := map[string]string{
		"DATABASE_URL": "postgres://localhost",
	}

	data := BuildRefData(captures, envrc)
	require.Equal(t, "https://example.com", data["tunnel.url"])
	require.Equal(t, "postgres://localhost", data["envrc.DATABASE_URL"])
}

func TestBuildRefData_Empty(t *testing.T) {
	data := BuildRefData(nil, nil)
	require.Empty(t, data)
}
