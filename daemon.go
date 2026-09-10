package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func runtimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return d
	}
	return os.TempDir()
}

func socketPath() string { return filepath.Join(runtimeDir(), appName+".sock") }
func statePath() string  { return filepath.Join(runtimeDir(), appName+".state") }

func writeState(state string) {
	if err := os.WriteFile(statePath(), []byte(state), 0o644); err != nil {
		logger.Debug("write state", "error", err)
	}
}

type commandReply struct {
	OK    bool   `json:"ok"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

type daemon struct {
	cfg *Config

	mu       sync.Mutex
	state    string // "idle" | "recording"
	cancel   context.CancelFunc
	stop     chan struct{}
	aborting bool
}

func newDaemon(cfg *Config) *daemon {
	return &daemon{cfg: cfg, state: "idle"}
}

// serve listens on the unix socket until ctx is cancelled.
func (d *daemon) serve(ctx context.Context) error {
	path := socketPath()
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("listen %s: %w", path, err)
	}
	defer os.Remove(path)

	writeState("idle")
	logger.Info("daemon listening", "socket", path)
	notify(d.cfg, "Doubao dictation ready", "Press F9 to dictate", "low")

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go d.handleConn(conn)
	}
}

// shutdown aborts any in-flight recording and resets state.
func (d *daemon) shutdown() {
	d.mu.Lock()
	cancel, stop := d.cancel, d.stop
	d.stop, d.cancel = nil, nil
	d.state = "idle"
	d.mu.Unlock()

	if stop != nil {
		close(stop)
	}
	if cancel != nil {
		cancel()
	}
	writeState("idle")
}

func (d *daemon) handleConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && line == "" {
		return
	}
	cmd := strings.TrimSpace(line)
	logger.Info("command received", "command", cmd)

	data, _ := json.Marshal(d.command(cmd))
	_, _ = conn.Write(append(data, '\n'))
}

func (d *daemon) command(cmd string) commandReply {
	switch cmd {
	case "ping", "status":
		return commandReply{OK: true, State: d.currentState()}
	case "start":
		return d.start()
	case "stop":
		return d.stopRecording()
	case "toggle":
		if d.currentState() == "idle" {
			return d.start()
		}
		return d.stopRecording()
	case "cancel":
		return d.cancelSession()
	default:
		return commandReply{OK: false, State: d.currentState(), Error: fmt.Sprintf("unknown command %q", cmd)}
	}
}

func (d *daemon) currentState() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}

func (d *daemon) start() commandReply {
	d.mu.Lock()
	if d.state != "idle" {
		d.mu.Unlock()
		return commandReply{OK: false, State: "recording", Error: "already active"}
	}
	ctx := context.Background()
	if d.cfg.MaxDuration > 0 {
		ctx, d.cancel = context.WithTimeout(ctx, d.cfg.MaxDuration)
	} else {
		ctx, d.cancel = context.WithCancel(ctx)
	}
	stop := make(chan struct{})
	d.state = "recording"
	d.stop = stop
	d.aborting = false
	d.mu.Unlock()

	writeState("recording")
	notify(d.cfg, "🎤 Recording", "Press F9 again to stop", "normal")
	go d.run(ctx, stop)
	return commandReply{OK: true, State: "recording"}
}

func (d *daemon) stopRecording() commandReply {
	d.mu.Lock()
	if d.state != "recording" || d.stop == nil {
		d.mu.Unlock()
		return commandReply{OK: false, State: d.state, Error: "not recording"}
	}
	stop := d.stop
	d.stop = nil
	d.mu.Unlock()

	close(stop)
	notify(d.cfg, "Transcribing…", "", "low")
	return commandReply{OK: true, State: "recording"}
}

func (d *daemon) cancelSession() commandReply {
	d.mu.Lock()
	if d.state != "recording" {
		d.mu.Unlock()
		return commandReply{OK: false, State: d.state, Error: "not recording"}
	}
	d.aborting = true
	cancel, stop := d.cancel, d.stop
	d.stop = nil
	d.mu.Unlock()

	if stop != nil {
		close(stop)
	}
	if cancel != nil {
		cancel()
	}
	return commandReply{OK: true, State: "recording"}
}

// run performs one dictation session and updates state when it finishes.
func (d *daemon) run(ctx context.Context, stop chan struct{}) {
	defer func() {
		d.mu.Lock()
		d.state = "idle"
		d.cancel = nil
		d.mu.Unlock()
		writeState("idle")
	}()

	text, err := runBatch(ctx, d.cfg, stop)

	d.mu.Lock()
	aborted := d.aborting
	d.mu.Unlock()
	if aborted {
		logger.Info("session cancelled")
		return
	}
	if err != nil {
		logger.Error("dictation failed", "error", err)
		notify(d.cfg, "Dictation failed", err.Error(), "critical")
		return
	}
	if strings.TrimSpace(text) == "" {
		logger.Info("no speech detected")
		notify(d.cfg, "No speech detected", "", "low")
		return
	}

	logger.Info("transcript", "text", text)
	outCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := outputText(outCtx, d.cfg, text); err != nil {
		logger.Error("output failed", "error", err)
		notify(d.cfg, "Dictation output failed", err.Error(), "critical")
	}
}
