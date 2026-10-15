package cli

import (
	"fmt"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Change agent-brain settings",
	}
	cmd.AddCommand(newConfigLinkOfferCmd())
	return cmd
}

// newConfigLinkOfferCmd shows or sets the session-start question asking whether
// an unlinked project should use agent-brain cloud. It is off by default.
func newConfigLinkOfferCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "link-offer [on|off]",
		Short: "Show or set the session-start question about agent-brain cloud (off by default)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if len(args) == 0 {
				cfg, err := config.Load()
				if err != nil {
					return err
				}
				state := "off"
				if cfg.LinkOfferEnabled() {
					state = "on"
				}
				fmt.Fprintf(out, "Link offer: %s\n", state)
				return nil
			}
			var on bool
			switch args[0] {
			case "on":
				on = true
			case "off":
				on = false
			default:
				return fmt.Errorf("link-offer must be \"on\" or \"off\", got %q", args[0])
			}
			if _, err := config.Update(func(c *config.Config) error {
				c.LinkOffer = &on
				return nil
			}); err != nil {
				return fmt.Errorf("save link offer: %w", err)
			}
			if on {
				fmt.Fprintln(out, "Link offer on: unlinked projects will ask at session start whether to use agent-brain cloud.")
			} else {
				fmt.Fprintln(out, "Link offer off: sessions will not ask about agent-brain cloud. Turn it on with `agent-brain config link-offer on`.")
			}
			return nil
		},
	}
}
