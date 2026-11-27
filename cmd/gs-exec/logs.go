package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type logStreamer struct {
	client   *http.Client
	bridge   string
	onCancel func()

	mu       sync.Mutex
	buf      []logLine
	canceled bool
	flushCh  chan struct{}
	stopCh   chan struct{}
	doneCh   chan struct{}
}

func newLogStreamer(client *http.Client, bridge string, onCancel func()) *logStreamer {
	s := &logStreamer{
		client:   client,
		bridge:   bridge,
		onCancel: onCancel,
		flushCh:  make(chan struct{}, 1),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
	go s.loop()
	return s
}

func (s *logStreamer) consume(stream string, r io.Reader) {
	buf := make([]byte, 8192)
	var pending strings.Builder
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])
			pending.WriteString(chunk)
			text := pending.String()
			for {
				idx := strings.IndexByte(text, '\n')
				if idx < 0 {
					break
				}
				line := strings.TrimSuffix(text[:idx], "\r")
				s.push(logLine{Stream: stream, Text: line})
				text = text[idx+1:]
			}
			pending.Reset()
			pending.WriteString(text)
			if pending.Len() > 4096 {
				s.push(logLine{Stream: stream, Text: pending.String()})
				pending.Reset()
			}
		}
		if err != nil {
			if pending.Len() > 0 {
				s.push(logLine{Stream: stream, Text: pending.String()})
			}
			return
		}
	}
}

func (s *logStreamer) push(line logLine) {
	s.mu.Lock()
	s.buf = append(s.buf, line)
	flushNow := len(s.buf) >= maxLogChunkLines
	s.mu.Unlock()
	if flushNow {
		// Signal loop() rather than calling flush() directly on reader
		// goroutines, so HTTP POST /logs requests stay strictly serialized
		// and in order.
		select {
		case s.flushCh <- struct{}{}:
		default:
		}
	}
}

func (s *logStreamer) loop() {
	defer close(s.doneCh)
	flushTicker := time.NewTicker(logFlushInterval)
	heartbeatTicker := time.NewTicker(heartbeatEvery)
	defer flushTicker.Stop()
	defer heartbeatTicker.Stop()
	for {
		select {
		case <-s.flushCh:
			s.flush(false)
		case <-flushTicker.C:
			s.flush(false)
		case <-heartbeatTicker.C:
			s.flush(true)
		case <-s.stopCh:
			s.flush(false)
			return
		}
	}
}

func (s *logStreamer) flush(forceHeartbeat bool) {
	s.mu.Lock()
	lines := s.buf
	s.buf = nil
	s.mu.Unlock()
	if len(lines) == 0 && !forceHeartbeat {
		return
	}
	for len(lines) > 0 || forceHeartbeat {
		batch := lines
		if len(batch) > maxLogChunkLines {
			batch = lines[:maxLogChunkLines]
			lines = lines[maxLogChunkLines:]
		} else {
			lines = nil
		}
		forceHeartbeat = false
		body, _ := json.Marshal(map[string]any{"lines": batch})
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.bridge+"/logs", bytes.NewReader(body))
		if err != nil {
			cancel()
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.client.Do(req)
		cancel()
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusPreconditionFailed || resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusGone {
			s.mu.Lock()
			s.canceled = true
			s.mu.Unlock()
			if s.onCancel != nil {
				s.onCancel()
			}
			return
		}
	}
}

func (s *logStreamer) wasCanceled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.canceled
}

func (s *logStreamer) close() {
	close(s.stopCh)
	<-s.doneCh
}

func postFinish(ctx context.Context, client *http.Client, bridge, outcome string, exitCode int, errText string) error {
	body, _ := json.Marshal(map[string]any{
		"outcome":   outcome,
		"exit_code": exitCode,
		"error":     errText,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bridge+"/finish", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// runHelperCLI implements the `gs-auto` convenience subcommands inside the
// container, talking exclusively to http://gitslice.internal/gs/* so shell
// scripts never need raw access to runToken.
func runHelperCLI(args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 {
		return errors.New("usage: gs-auto <check|state|set-output|set-details-url> ...")
	}
	bridge := bridgeURL()
	client := &http.Client{Timeout: 30 * time.Second}

	switch args[0] {
	case "set-output":
		if len(args) < 3 {
			return errors.New("usage: gs-auto set-output <key> <value>")
		}
		return callBridgeJSON(client, bridge+"/gs/output/set", map[string]any{
			"key": args[1], "value": args[2],
		}, stdout)
	case "set-details-url":
		return callBridgeJSON(client, bridge+"/gs/details-url/set", map[string]any{
			"detailsUrl": args[1],
		}, stdout)
	case "check":
		switch args[1] {
		case "create":
			fs := flag.NewFlagSet("gs-auto check create", flag.ContinueOnError)
			fs.SetOutput(stderr)
			name := fs.String("name", "", "check name")
			status := fs.String("status", "running", "check status")
			summary := fs.String("summary", "", "check summary")
			detailsURL := fs.String("details-url", "", "details url")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if strings.TrimSpace(*name) == "" {
				return errors.New("--name is required")
			}
			return callBridgeJSON(client, bridge+"/gs/check/create", map[string]any{
				"name": *name, "status": *status, "summary": *summary, "detailsUrl": *detailsURL,
			}, stdout)
		case "update":
			if len(args) < 3 {
				return errors.New("usage: gs-auto check update <check-run-id> [--status ...] [--summary ...] [--details-url ...]")
			}
			checkID := args[2]
			fs := flag.NewFlagSet("gs-auto check update", flag.ContinueOnError)
			fs.SetOutput(stderr)
			status := fs.String("status", "", "check status")
			summary := fs.String("summary", "", "check summary")
			detailsURL := fs.String("details-url", "", "details url")
			if err := fs.Parse(args[3:]); err != nil {
				return err
			}
			payload := map[string]any{"checkRunId": checkID}
			if *status != "" {
				payload["status"] = *status
			}
			if *summary != "" {
				payload["summary"] = *summary
			}
			if *detailsURL != "" {
				payload["detailsUrl"] = *detailsURL
			}
			return callBridgeJSON(client, bridge+"/gs/check/update", payload, stdout)
		default:
			return fmt.Errorf("unknown check subcommand %q", args[1])
		}
	case "state":
		switch args[1] {
		case "get":
			return callBridgeJSON(client, bridge+"/gs/state/get", map[string]any{}, stdout)
		case "set":
			if len(args) < 3 {
				return errors.New("usage: gs-auto state set <json>")
			}
			return callBridgeJSON(client, bridge+"/gs/state/set", map[string]any{"stateJson": args[2]}, stdout)
		default:
			return fmt.Errorf("unknown state subcommand %q", args[1])
		}
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func callBridgeJSON(client *http.Client, url string, payload any, stdout io.Writer) error {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("bridge HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if len(data) > 0 {
		_, _ = stdout.Write(data)
		if data[len(data)-1] != '\n' {
			_, _ = stdout.Write([]byte("\n"))
		}
	}
	return nil
}
