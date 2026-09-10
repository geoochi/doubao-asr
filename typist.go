package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// outputText delivers the transcript to the focused window, either by typing it
// (wtype) or by putting it on the clipboard (wl-copy).
func outputText(ctx context.Context, cfg *Config, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	switch cfg.Mode {
	case "clipboard":
		return runWithStdin(ctx, text, "wl-copy")
	default:
		// `wtype -` types whatever it reads from stdin.
		return runWithStdin(ctx, text, "wtype", "-")
	}
}

func runWithStdin(ctx context.Context, input string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w (%s)", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
