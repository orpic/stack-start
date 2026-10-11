package cli

import (
	_ "embed"
	"fmt"

	"github.com/spf13/cobra"
)

//go:embed schema/stackstart.schema.json
var configSchema string

func newSchemaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "schema",
		Short: "Print the JSON Schema of stackstart.yaml (for editors and agents)",
		Long: `Print the JSON Schema of stackstart.yaml. Editors with the YAML language
server can use it for completion and checking: save it and add
"# yaml-language-server: $schema=stackstart.schema.json" at the top of
stackstart.yaml.`,
		Example: `  stackstart schema > stackstart.schema.json`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Print(configSchema)
			return nil
		},
	}
}
