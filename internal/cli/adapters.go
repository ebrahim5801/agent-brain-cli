package cli

// Blank imports register every assistant adapter into assistant.Registry at
// package load. The CLI (install, uninstall, status, hook) then talks only to
// the assistant interface and registry — no adapter package is referenced by
// name outside these imports (constitution VI). Claude is imported directly
// elsewhere too; listing it here keeps the registered set in one place.
import (
	_ "github.com/ebrahim5801/agent-brain-cli/internal/assistant/claude"
	_ "github.com/ebrahim5801/agent-brain-cli/internal/assistant/codex"
	_ "github.com/ebrahim5801/agent-brain-cli/internal/assistant/copilot"
	_ "github.com/ebrahim5801/agent-brain-cli/internal/assistant/cursor"
	_ "github.com/ebrahim5801/agent-brain-cli/internal/assistant/gemini"
	_ "github.com/ebrahim5801/agent-brain-cli/internal/assistant/opencode"
)
