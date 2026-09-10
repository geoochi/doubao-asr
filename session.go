package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"

	"github.com/coder/websocket"
)

// runBatch records the microphone until stop is closed, then sends the whole
// utterance to the non-stream endpoint and returns the recognised text.
func runBatch(ctx context.Context, cfg *Config, stop <-chan struct{}) (string, error) {
	recCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-stop:
		case <-recCtx.Done():
		}
		cancel()
	}()

	pcm, err := record(recCtx, cfg)
	if err != nil {
		return "", err
	}
	if len(pcm) == 0 {
		return "", nil
	}
	logger.Info("recorded audio", "seconds", float64(len(pcm))/2/float64(cfg.SampleRate))
	return transcribe(ctx, cfg, pcm)
}

// record spawns parecord and buffers raw PCM until the context is cancelled
// (which kills the process) or the recorder exits.
func record(ctx context.Context, cfg *Config) ([]byte, error) {
	args := []string{
		"--raw", "--format=s16le",
		fmt.Sprintf("--rate=%d", cfg.SampleRate),
		"--channels=1", "--latency-msec=100",
	}
	if cfg.Device != "" && cfg.Device != "default" {
		args = append(args, "-d", cfg.Device)
	}
	cmd := exec.CommandContext(ctx, "parecord", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("parecord stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start parecord: %w", err)
	}

	var buf bytes.Buffer
	chunk := make([]byte, cfg.segmentBytes())
	for {
		n, err := io.ReadFull(stdout, chunk)
		if n > 0 {
			buf.Write(chunk[:n])
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				logger.Debug("recorder read ended", "error", err)
			}
			break
		}
	}
	_ = cmd.Wait()
	return buf.Bytes(), nil
}

// transcribe sends the buffered PCM and returns the final transcript.
func transcribe(ctx context.Context, cfg *Config, pcm []byte) (string, error) {
	if cfg.APIKey == "" {
		return "", errors.New("no API key configured (set DOUBAO_API_KEY)")
	}

	hdrs := http.Header{}
	for k, v := range authHeaders(cfg.ResourceID, cfg.APIKey) {
		hdrs.Set(k, v)
	}
	conn, _, err := websocket.Dial(ctx, cfg.URL, &websocket.DialOptions{HTTPHeader: hdrs})
	if err != nil {
		return "", fmt.Errorf("dial %s: %w", cfg.URL, err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(8 << 20)
	logger.Info("connected", "url", cfg.URL)

	full, err := fullClientRequest(1, sessionPayload(cfg.SampleRate))
	if err != nil {
		return "", err
	}
	if err := conn.Write(ctx, websocket.MessageBinary, full); err != nil {
		return "", fmt.Errorf("send config: %w", err)
	}
	if _, _, err := conn.Read(ctx); err != nil {
		return "", fmt.Errorf("read ack: %w", err)
	}

	// Send audio in the background while draining responses: the server replies
	// to every audio frame, and not reading them can stall the connection.
	seg := cfg.segmentBytes()
	sendErr := make(chan error, 1)
	go func() {
		seq := int32(2)
		for off := 0; off < len(pcm); off += seg {
			end := min(off+seg, len(pcm))
			last := end >= len(pcm)
			frame, err := audioOnlyRequest(seq, pcm[off:end], last)
			if err != nil {
				sendErr <- err
				return
			}
			if err := conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
				sendErr <- fmt.Errorf("send audio: %w", err)
				return
			}
			if !last {
				seq++
			}
		}
		sendErr <- nil
	}()

	var text string
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			select {
			case se := <-sendErr:
				if se != nil {
					return "", se
				}
			default:
			}
			return "", fmt.Errorf("read response: %w", err)
		}
		resp, err := parseResponse(data)
		if err != nil {
			return "", err
		}
		if resp.Code != 0 {
			return "", fmt.Errorf("ASR error %d: %v", resp.Code, resp.Raw)
		}
		if resp.Text != "" {
			text = resp.Text
		}
		if resp.Last {
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return text, nil
		}
	}
}
