package cli

import (
	"context"
	"os"

	"charm.land/huh/v2/spinner"
	"github.com/spf13/cobra"

	"github.com/rossedman/strata/internal/ui"
)

// Spin runs action with a spinner when stderr is a terminal, and plainly
// otherwise. STRATA_ACCESSIBLE=1 switches to huh's screen-reader mode.
func Spin(ctx context.Context, cmd *cobra.Command, title string, action func(context.Context) error) error {
	if !ui.IsTerminal(cmd.ErrOrStderr()) {
		return action(ctx)
	}
	return spinner.New().
		Title(title).
		WithOutput(cmd.ErrOrStderr()).
		WithAccessible(os.Getenv("STRATA_ACCESSIBLE") != "").
		Context(ctx).
		ActionWithErr(action).
		Run()
}
