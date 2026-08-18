// Package daemon installs the collector's background sync service as a
// user-level unit: launchd agent (macOS), systemd user unit (Linux),
// Task Scheduler logon task (Windows). No root, no custom supervisor.
// Installed by `agent-brain login`, removed by `agent-brain logout` — plain
// `install` stays cloud-free (research R8).
package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const serviceName = "agent-brain-sync"

// Install registers and starts the background service for the given binary.
// Best-effort by contract: an environment without a service manager (e.g.
// containers, CI) returns an error the caller reports as a hint, not a failure.
func Install(binPath string) error {
	switch runtime.GOOS {
	case "darwin":
		return installLaunchd(binPath)
	case "linux":
		return installSystemdUser(binPath)
	case "windows":
		return installSchtasks(binPath)
	default:
		return fmt.Errorf("no background service support for %s; run `agent-brain sync` manually", runtime.GOOS)
	}
}

func Remove() error {
	switch runtime.GOOS {
	case "darwin":
		return removeLaunchd()
	case "linux":
		return removeSystemdUser()
	case "windows":
		return removeSchtasks()
	default:
		return nil
	}
}

// Running reports whether the service is active, when that can be determined.
func Running() (bool, bool) {
	switch runtime.GOOS {
	case "linux":
		out, err := exec.Command("systemctl", "--user", "is-active", serviceName+".service").Output()
		if err != nil {
			return false, true
		}
		return strings.TrimSpace(string(out)) == "active", true
	case "darwin":
		err := exec.Command("launchctl", "list", "dev.agent-brain.sync").Run()
		return err == nil, true
	default:
		return false, false
	}
}

func installLaunchd(binPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	plist := filepath.Join(dir, "dev.agent-brain.sync.plist")
	content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>dev.agent-brain.sync</string>
    <key>ProgramArguments</key>
    <array><string>%s</string><string>daemon</string></array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
</dict>
</plist>
`, binPath)
	if err := os.WriteFile(plist, []byte(content), 0o644); err != nil {
		return err
	}
	_ = exec.Command("launchctl", "unload", plist).Run()
	return exec.Command("launchctl", "load", plist).Run()
}

func removeLaunchd() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", "dev.agent-brain.sync.plist")
	_ = exec.Command("launchctl", "unload", plist).Run()
	err = os.Remove(plist)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func systemdUnitPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "systemd", "user", serviceName+".service"), nil
}

func installSystemdUser(binPath string) error {
	unitPath, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf(`[Unit]
Description=agent-brain background sync

[Service]
ExecStart=%s daemon
Restart=on-failure
RestartSec=30

[Install]
WantedBy=default.target
`, binPath)
	if err := os.WriteFile(unitPath, []byte(content), 0o644); err != nil {
		return err
	}
	if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("systemd user session unavailable; run `agent-brain sync` manually or start `agent-brain daemon` yourself")
	}
	return exec.Command("systemctl", "--user", "enable", "--now", serviceName+".service").Run()
}

func removeSystemdUser() error {
	unitPath, err := systemdUnitPath()
	if err != nil {
		return err
	}
	_ = exec.Command("systemctl", "--user", "disable", "--now", serviceName+".service").Run()
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	err = os.Remove(unitPath)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func installSchtasks(binPath string) error {
	_ = exec.Command("schtasks", "/Delete", "/TN", serviceName, "/F").Run()
	return exec.Command("schtasks", "/Create",
		"/TN", serviceName,
		"/TR", fmt.Sprintf(`"%s" daemon`, binPath),
		"/SC", "ONLOGON", "/RL", "LIMITED", "/F").Run()
}

func removeSchtasks() error {
	_ = exec.Command("schtasks", "/End", "/TN", serviceName).Run()
	return exec.Command("schtasks", "/Delete", "/TN", serviceName, "/F").Run()
}
