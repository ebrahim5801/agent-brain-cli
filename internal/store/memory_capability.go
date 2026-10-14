package store

import "errors"

var ErrMemoryRestartRequired = errors.New("memory schema changed: restart MCP/daemon processes before serving or saving memory")

// AssertMemoryCapability makes a pre-upgrade long-lived reader fail closed.
// It must never serve physical legacy statuses after overlays become active.
func (s *Store) AssertMemoryCapability() error {
	version, err := s.SchemaVersion()
	if err != nil {
		return err
	}
	if version > 16 || (version >= 16) != s.MemoryV2 {
		return ErrMemoryRestartRequired
	}
	return nil
}
