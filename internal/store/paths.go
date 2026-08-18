package store

import (
	"os"
	"path/filepath"
	"runtime"
)

// DataDir resolves the OS-appropriate per-user data directory.
// AGENT_BRAIN_DATA_DIR overrides it (used by tests).
func DataDir() (string, error) {
	if v := os.Getenv("AGENT_BRAIN_DATA_DIR"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "agent-brain"), nil
	case "windows":
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, "agent-brain"), nil
		}
		return filepath.Join(home, "AppData", "Local", "agent-brain"), nil
	default:
		if v := os.Getenv("XDG_DATA_HOME"); v != "" {
			return filepath.Join(v, "agent-brain"), nil
		}
		return filepath.Join(home, ".local", "share", "agent-brain"), nil
	}
}

func DBPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agent-brain.db"), nil
}

func DiagLogPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "diagnostics.log"), nil
}
