package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const appName = "doubao-dictate"

// logger is usable before main() runs (e.g. from tests); main() may raise its level.
var logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

func main() {
	if strings.EqualFold(os.Getenv("DOUBAO_LOG_LEVEL"), "debug") {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	cmd := "daemon"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "daemon":
		os.Exit(runDaemon())
	case "start", "stop", "toggle", "cancel", "status", "ping":
		os.Exit(runClient(cmd))
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `Doubao batch dictation.

Usage:
  doubao-dictate daemon                 run the background daemon
  doubao-dictate start|stop|toggle      control recording
  doubao-dictate cancel                 discard the current recording
  doubao-dictate status                 print the daemon state
`)
}

func runDaemon() int {
	cfg, err := loadConfig()
	if err != nil {
		logger.Error("load config", "error", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	d := newDaemon(cfg)
	err = d.serve(ctx)
	d.shutdown()
	if err != nil {
		logger.Error("daemon", "error", err)
		return 1
	}
	return 0
}

// runClient sends one command to a running daemon and prints its JSON reply.
func runClient(cmd string) int {
	conn, err := net.DialTimeout("unix", socketPath(), 3*time.Second)
	if err != nil {
		fmt.Println(`{"ok":false,"state":"","error":"daemon not running"}`)
		return 1
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := fmt.Fprintf(conn, "%s\n", cmd); err != nil {
		fmt.Println(`{"ok":false,"state":"","error":"send failed"}`)
		return 1
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && line == "" {
		fmt.Println(`{"ok":false,"state":"","error":"read failed"}`)
		return 1
	}

	line = strings.TrimSpace(line)
	var reply commandReply
	_ = json.Unmarshal([]byte(line), &reply)
	fmt.Println(line)
	if reply.OK {
		return 0
	}
	return 1
}
