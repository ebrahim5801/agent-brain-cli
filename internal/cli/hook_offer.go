package cli

import (
	"fmt"

	"github.com/ebrahim5801/agent-brain-cli/internal/attribution"
	"github.com/ebrahim5801/agent-brain-cli/internal/config"
	"github.com/ebrahim5801/agent-brain-cli/internal/diag"
)

// linkOfferNote returns a session-start context note offering to link an
// unlinked project directory, or "" when no offer applies: not signed in
// (--auto would just fail), already linked, or previously declined. Hooks run
// non-interactively, so the offer is assistant-mediated — the note asks the
// assistant to relay one yes/no question and record the answer via
// `link --auto` or `link --decline`; a decline is permanent (FR: never
// re-ask) and local collection continues either way.
func linkOfferNote(logger *diag.Logger, cwd string) string {
	cfg, err := config.Load()
	if err != nil {
		logger.Log("hook", "link offer: load config: %v", err)
		return ""
	}
	if !cfg.SignedIn() {
		return ""
	}
	proj := attribution.Resolve(cwd)
	linkKey := config.LinkKey(proj.Kind, proj.Identity)
	if _, ok := cfg.Links[linkKey]; ok {
		return ""
	}
	if _, ok := cfg.LinkDeclines[linkKey]; ok {
		return ""
	}
	return fmt.Sprintf(`## agent-brain: project not linked

%s is not linked to an agent-brain cloud project, so its sessions stay on this
machine only. Before doing anything else this session, ask the user exactly one
question: "Use agent-brain cloud for this project? It will create a project
named %q on your account and sync sessions to it (yes/no)."
- If they answer yes, run: agent-brain link --auto
- If they answer no, run: agent-brain link --decline
  (records the choice so they are never asked again; local collection continues)
Then continue with whatever they asked for. Do not repeat the question later in
the session, and do not link without an explicit yes.
`, proj.DisplayName, proj.DisplayName)
}
