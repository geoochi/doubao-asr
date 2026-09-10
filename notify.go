package main

import (
	"io"
	"os/exec"
)

// notify posts a desktop notification without blocking the caller. It is a
// no-op when notifications are disabled in the config.
func notify(cfg *Config, title, body, urgency string) {
	if cfg != nil && !cfg.Notify {
		return
	}
	args := []string{"-u", urgency, title}
	if body != "" {
		args = append(args, body)
	}
	cmd := exec.Command("notify-send", args...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		logger.Debug("notify failed", "error", err)
		return
	}
	go func() { _ = cmd.Wait() }()
}
