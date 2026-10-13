package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ebrahim5801/agent-brain-cli/internal/config"
)

// newMemoryCaptureCmd configures when the automatic memory-capture prompts fire
// (memory_capture in the global config). With no flags it prints the settings.
func newMemoryCaptureCmd() *cobra.Command {
	var every, checkpoint int
	var reset bool
	cmd := &cobra.Command{
		Use:   "capture",
		Short: "Show or set when automatic memory capture prompts the assistant to save",
		Long: "Show or set when agent-brain asks the assistant to save memory automatically.\n" +
			"--every sets how many real tool calls (memory-server calls excluded) since the\n" +
			"last save arm the end-of-reply save; --checkpoint sets the same for the\n" +
			"mid-task checkpoint. 0 turns that trigger off. --reset restores the defaults.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			everySet := cmd.Flags().Changed("every")
			checkpointSet := cmd.Flags().Changed("checkpoint")
			if reset && (everySet || checkpointSet) {
				return fmt.Errorf("--reset cannot be combined with --every or --checkpoint")
			}
			if everySet && every < 0 {
				return fmt.Errorf("--every must be 0 or more")
			}
			if checkpointSet && checkpoint < 0 {
				return fmt.Errorf("--checkpoint must be 0 or more")
			}
			if reset || everySet || checkpointSet {
				if _, err := config.Update(func(c *config.Config) error {
					return applyMemoryCapture(c, reset, everySet, every, checkpointSet, checkpoint)
				}); err != nil {
					return err
				}
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			printMemoryCapture(cmd.OutOrStdout(), cfg)
			return nil
		},
	}
	cmd.Flags().IntVar(&every, "every", 0, "end-of-reply save: real tool calls since the last save (0 = off)")
	cmd.Flags().IntVar(&checkpoint, "checkpoint", 0, "mid-task checkpoint: real tool calls since the last checkpoint (0 = off)")
	cmd.Flags().BoolVar(&reset, "reset", false, "restore the default capture settings")
	return cmd
}

// applyMemoryCapture writes the requested thresholds into the config. Each
// setter touches only its own field; --reset clears the whole block so both
// triggers return to their defaults.
func applyMemoryCapture(c *config.Config, reset, everySet bool, every int, checkpointSet bool, checkpoint int) error {
	if reset {
		c.MemoryCapture = nil
		return nil
	}
	if c.MemoryCapture == nil {
		c.MemoryCapture = &config.MemoryCaptureConfig{}
	}
	if everySet {
		v := every
		c.MemoryCapture.Every = &v
	}
	if checkpointSet {
		v := checkpoint
		c.MemoryCapture.Checkpoint = &v
	}
	return nil
}

// printMemoryCapture renders the effective capture settings, marking defaults.
func printMemoryCapture(w io.Writer, cfg *config.Config) {
	every := cfg.MemoryEvery()
	checkpoint := cfg.MemoryCheckpoint(config.DefaultMemoryCheckpoint)
	fmt.Fprintf(w, "End-of-reply save: %s\n", describeCaptureTrigger(every, config.DefaultMemoryEvery, "after %d tool calls"))
	fmt.Fprintf(w, "Mid-task checkpoint: %s\n", describeCaptureTrigger(checkpoint, config.DefaultMemoryCheckpoint, "every %d tool calls"))
}

// describeCaptureTrigger formats one trigger: "off" for 0, otherwise the
// threshold (phrased by format) with "(default)" when it equals the default.
func describeCaptureTrigger(value, def int, format string) string {
	if value == 0 {
		return "off"
	}
	s := fmt.Sprintf(format, value)
	if value == def {
		s += " (default)"
	}
	return s
}
