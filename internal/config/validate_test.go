package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func validProfile() Profile {
	return Profile{
		Processes: map[string]Process{
			"db": {
				Cwd: "packages/db",
				Cmd: "docker compose up",
				Readiness: &Readiness{
					Timeout: Duration{Duration: 30 * time.Second},
					Checks:  []Check{{TCP: "localhost:5432"}},
				},
			},
			"backend": {
				Cwd:       "packages/backend",
				Cmd:       "npm run dev",
				DependsOn: []string{"db"},
				Readiness: &Readiness{
					Timeout: Duration{Duration: 60 * time.Second},
					Checks:  []Check{{Log: "listening on port"}},
				},
			},
		},
	}
}

func TestValidate_ValidProfile(t *testing.T) {
	require.NoError(t, Validate(validProfile(), "dev"))
}

func TestValidate_NoProcesses(t *testing.T) {
	p := Profile{Processes: map[string]Process{}}
	err := Validate(p, "empty")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no processes defined")
}

func TestValidate_MissingCmd(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"broken": {Cwd: "."},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "'cmd' is required")
}

func TestValidate_AbsoluteCwd(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"broken": {Cwd: "/absolute/path", Cmd: "echo hi"},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "absolute path")
}

func TestValidate_UnknownDependency(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"app": {Cmd: "echo hi", DependsOn: []string{"nonexistent"}},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown process")
}

func TestValidate_SelfDependency(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"app": {Cmd: "echo hi", DependsOn: []string{"app"}},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot depend on itself")
}

func TestValidate_InvalidOnExit(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"app": {Cmd: "echo hi", OnExit: "restart"},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "on_exit must be")
}

func TestValidate_CycleDetection_Simple(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"a": {Cmd: "echo a", DependsOn: []string{"b"}},
			"b": {Cmd: "echo b", DependsOn: []string{"a"}},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "cycle detected")
}

func TestValidate_CycleDetection_ThreeWay(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"a": {Cmd: "echo a", DependsOn: []string{"b"}},
			"b": {Cmd: "echo b", DependsOn: []string{"c"}},
			"c": {Cmd: "echo c", DependsOn: []string{"a"}},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "cycle detected")
}

func TestValidate_NoCycle_Diamond(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"db":      {Cmd: "echo db"},
			"cache":   {Cmd: "echo cache"},
			"backend": {Cmd: "echo backend", DependsOn: []string{"db", "cache"}},
			"web":     {Cmd: "echo web", DependsOn: []string{"backend"}},
		},
	}
	require.NoError(t, Validate(p, "test"))
}

func TestValidate_ReadinessNoTimeout(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"app": {
				Cmd: "echo hi",
				Readiness: &Readiness{
					Checks: []Check{{Log: "ready"}},
				},
			},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "timeout is required")
}

func TestValidate_ReadinessNoChecks(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"app": {
				Cmd: "echo hi",
				Readiness: &Readiness{
					Timeout: Duration{Duration: 5 * time.Second},
					Checks:  []Check{},
				},
			},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least one check")
}

func TestValidate_ReadinessBadMode(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"app": {
				Cmd: "echo hi",
				Readiness: &Readiness{
					Timeout: Duration{Duration: 5 * time.Second},
					Mode:    "first",
					Checks:  []Check{{Log: "ready"}},
				},
			},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "mode must be")
}

func TestValidate_ReadinessEmptyCheck(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"app": {
				Cmd: "echo hi",
				Readiness: &Readiness{
					Timeout: Duration{Duration: 5 * time.Second},
					Checks:  []Check{{}},
				},
			},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "must specify either")
}

func TestValidate_ReadinessBothLogAndTCP(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"app": {
				Cmd: "echo hi",
				Readiness: &Readiness{
					Timeout: Duration{Duration: 5 * time.Second},
					Checks:  []Check{{Log: "ready", TCP: "localhost:3000"}},
				},
			},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "only one of")
}

func TestValidate_ReadinessBadRegex(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"app": {
				Cmd: "echo hi",
				Readiness: &Readiness{
					Timeout: Duration{Duration: 5 * time.Second},
					Checks:  []Check{{Log: "[invalid"}},
				},
			},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid log regex")
}

func TestValidate_ReadinessTCPNoPort(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"app": {
				Cmd: "echo hi",
				Readiness: &Readiness{
					Timeout: Duration{Duration: 5 * time.Second},
					Checks:  []Check{{TCP: "localhost"}},
				},
			},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "host:port")
}

func TestValidate_CaptureMissingName(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"app": {
				Cmd:      "echo hi",
				Captures: []Capture{{Log: "(.*)", Name: ""}},
			},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing 'name'")
}

func TestValidate_CaptureMissingLog(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"app": {
				Cmd:      "echo hi",
				Captures: []Capture{{Name: "url", Log: ""}},
			},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing 'log'")
}

func TestValidate_CaptureWrongGroupCount(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"app": {
				Cmd:      "echo hi",
				Captures: []Capture{{Name: "url", Log: "no-groups-here"}},
			},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "exactly 1 capturing group")
}

func TestValidate_CaptureTwoGroups(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"app": {
				Cmd:      "echo hi",
				Captures: []Capture{{Name: "url", Log: "(a)(b)"}},
			},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "exactly 1 capturing group, got 2")
}

func TestValidate_CaptureRefValid(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"producer": {
				Cmd:      "echo hi",
				Captures: []Capture{{Name: "url", Log: "(https://.*)"}},
			},
			"consumer": {
				Cmd: "echo hi",
				Env: map[string]string{"URL": "${producer.url}"},
			},
		},
	}
	require.NoError(t, Validate(p, "test"))
}

func TestValidate_CaptureRefUnknownProcess(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"consumer": {
				Cmd: "echo hi",
				Env: map[string]string{"URL": "${ghost.url}"},
			},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown process")
}

func TestValidate_CaptureRefUnknownCapture(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"producer": {
				Cmd:      "echo hi",
				Captures: []Capture{{Name: "url", Log: "(https://.*)"}},
			},
			"consumer": {
				Cmd: "echo hi",
				Env: map[string]string{"URL": "${producer.missing}"},
			},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown capture")
}

func TestValidate_EnvrcRefSkipped(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"app": {
				Cmd: "echo hi",
				Env: map[string]string{"DB": "${envrc.DATABASE_URL}"},
			},
		},
	}
	require.NoError(t, Validate(p, "test"))
}

func TestValidate_TemplateMissingSrc(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"app": {
				Cmd:       "echo hi",
				Templates: []Template{{Dst: "out.json"}},
			},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing 'src'")
}

func TestValidate_TemplateMissingDst(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"app": {
				Cmd:       "echo hi",
				Templates: []Template{{Src: "in.tmpl"}},
			},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing 'dst'")
}

func TestValidate_TemplateAbsoluteSrc(t *testing.T) {
	p := Profile{
		Processes: map[string]Process{
			"app": {
				Cmd:       "echo hi",
				Templates: []Template{{Src: "/absolute/in.tmpl", Dst: "out.json"}},
			},
		},
	}
	err := Validate(p, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be relative")
}
