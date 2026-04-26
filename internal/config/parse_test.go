package config

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseFile_ValidFull(t *testing.T) {
	f, err := ParseFile(filepath.Join("testdata", "valid_full.yaml"))
	require.NoError(t, err)
	require.Len(t, f.Profiles, 2)

	dev := f.Profiles["dev"]
	require.Len(t, dev.Processes, 4)

	db := dev.Processes["db"]
	require.Equal(t, "packages/db", db.Cwd)
	require.Equal(t, "docker compose up postgres", db.Cmd)
	require.NotNil(t, db.Readiness)
	require.Equal(t, 30*time.Second, db.Readiness.Timeout.Duration)
	require.Len(t, db.Readiness.Checks, 1)
	require.Equal(t, "localhost:5432", db.Readiness.Checks[0].TCP)

	backend := dev.Processes["backend"]
	require.Equal(t, []string{"db"}, backend.DependsOn)
	require.Equal(t, "${envrc.DATABASE_URL}", backend.Env["DATABASE_URL"])
	require.Equal(t, "listening on port 4000", backend.Readiness.Checks[0].Log)

	tunnel := dev.Processes["tunnel"]
	require.Len(t, tunnel.Captures, 1)
	require.Equal(t, "url", tunnel.Captures[0].Name)
	require.True(t, tunnel.Captures[0].IsRequired())

	web := dev.Processes["web"]
	require.Equal(t, "ignore", web.OnExit)
	require.Equal(t, "any", web.Readiness.Mode)
	require.Len(t, web.Readiness.Checks, 2)

	minimal := f.Profiles["minimal"]
	require.Len(t, minimal.Processes, 1)
}

func TestParseFile_NoProfiles(t *testing.T) {
	_, err := ParseFile(filepath.Join("testdata", "invalid_no_profiles.yaml"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "no 'profiles' key found")
}

func TestParseFile_BadYAML(t *testing.T) {
	_, err := ParseFile(filepath.Join("testdata", "invalid_bad_yaml.yaml"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "parsing")
}

func TestParseFile_NotFound(t *testing.T) {
	_, err := ParseFile("testdata/nonexistent.yaml")
	require.Error(t, err)
	require.Contains(t, err.Error(), "reading")
}

func TestDuration_Unmarshal(t *testing.T) {
	f, err := ParseFile(filepath.Join("testdata", "valid_full.yaml"))
	require.NoError(t, err)

	db := f.Profiles["dev"].Processes["db"]
	require.Equal(t, 30*time.Second, db.Readiness.Timeout.Duration)

	backend := f.Profiles["dev"].Processes["backend"]
	require.Equal(t, 60*time.Second, backend.Readiness.Timeout.Duration)
}

func TestProcess_Defaults(t *testing.T) {
	proc := Process{}
	require.True(t, proc.IsRequired())
	require.Equal(t, "fail", proc.OnExitPolicy())
	require.Equal(t, 10*time.Second, proc.GracePeriod())
}

func TestProcess_ExplicitValues(t *testing.T) {
	f := false
	proc := Process{
		Required:        &f,
		OnExit:          "ignore",
		StopGracePeriod: Duration{Duration: 5 * time.Second},
	}
	require.False(t, proc.IsRequired())
	require.Equal(t, "ignore", proc.OnExitPolicy())
	require.Equal(t, 5*time.Second, proc.GracePeriod())
}

func TestCapture_IsRequired_Default(t *testing.T) {
	c := Capture{Name: "url", Log: "(https://.*)", Required: nil}
	require.True(t, c.IsRequired())
}

func TestCapture_IsRequired_Explicit(t *testing.T) {
	f := false
	c := Capture{Name: "url", Log: "(https://.*)", Required: &f}
	require.False(t, c.IsRequired())
}

func TestReadiness_CheckMode_Default(t *testing.T) {
	r := Readiness{Mode: ""}
	require.Equal(t, "all", r.CheckMode())
}

func TestReadiness_CheckMode_Explicit(t *testing.T) {
	r := Readiness{Mode: "any"}
	require.Equal(t, "any", r.CheckMode())
}
