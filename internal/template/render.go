package template

import (
	"fmt"
	"os"
	"path/filepath"
	"text/template"
)

// Render reads the template at srcPath, executes it with the provided data,
// and writes the result to dstPath. Both paths are relative to projectPath.
func Render(projectPath, srcPath, dstPath string, data map[string]any) error {
	absSrc := filepath.Join(projectPath, srcPath)
	absDst := filepath.Join(projectPath, dstPath)

	content, err := os.ReadFile(absSrc)
	if err != nil {
		return fmt.Errorf("reading template %s: %w", absSrc, err)
	}

	tmpl, err := template.New(filepath.Base(srcPath)).Parse(string(content))
	if err != nil {
		return fmt.Errorf("parsing template %s: %w", absSrc, err)
	}

	if err := os.MkdirAll(filepath.Dir(absDst), 0755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", absDst, err)
	}

	f, err := os.Create(absDst)
	if err != nil {
		return fmt.Errorf("creating %s: %w", absDst, err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, data); err != nil {
		return fmt.Errorf("executing template %s: %w", absSrc, err)
	}

	return nil
}
