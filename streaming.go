package featureflip

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// streamStatusError is returned when the SSE endpoint responds with a non-200
// status code, carrying the status code for retry decisions.
type streamStatusError struct {
	statusCode int
}

func (e *streamStatusError) Error() string {
	return fmt.Sprintf("featureflip: stream: unexpected status %d", e.statusCode)
}

// streamSource connects to the evaluation API's SSE stream and updates the
// store in real time when flags or segments change.
type streamSource struct {
	hc             *httpClient
	store          *store
	onUpdate       func(key string)
	ctx            context.Context
	cancel         context.CancelFunc
	reconnectDelay time.Duration
	connectTimeout time.Duration
}

// newStreamSource creates a new SSE stream source.
func newStreamSource(hc *httpClient, store *store, onUpdate func(key string)) *streamSource {
	ctx, cancel := context.WithCancel(context.Background())
	return &streamSource{
		hc:             hc,
		store:          store,
		onUpdate:       onUpdate,
		ctx:            ctx,
		cancel:         cancel,
		reconnectDelay: 3 * time.Second,
		connectTimeout: 5 * time.Second,
	}
}

// run starts the SSE connection loop. On disconnect, it waits reconnectDelay
// then reconnects. Runs until stop() is called. Permanent errors (4xx) stop
// retrying; transient errors (5xx) continue with the reconnect delay.
func (ss *streamSource) run() {
	for {
		err := ss.connect()

		select {
		case <-ss.ctx.Done():
			return
		default:
		}

		// Stop retrying on permanent client errors (4xx).
		if isPermanentError(err) {
			return
		}

		// Wait before reconnecting.
		select {
		case <-ss.ctx.Done():
			return
		case <-time.After(ss.reconnectDelay):
		}
	}
}

// isPermanentError returns true if the error indicates a non-retryable HTTP
// status code (4xx range), excluding 408 and 429 which are transient.
func isPermanentError(err error) bool {
	var se *streamStatusError
	if errors.As(err, &se) {
		if se.statusCode == http.StatusRequestTimeout || se.statusCode == http.StatusTooManyRequests {
			return false
		}
		return se.statusCode >= 400 && se.statusCode < 500
	}
	return false
}

// connect opens a single SSE connection and reads events until the connection
// closes or the context is cancelled. Returns an error for non-2xx responses
// so the caller can decide whether to retry.
func (ss *streamSource) connect() error {
	req, err := ss.hc.newStreamRequest()
	if err != nil {
		return err
	}
	req = req.WithContext(ss.ctx)

	client := ss.hc.streamHTTPClient(ss.connectTimeout)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return &streamStatusError{statusCode: resp.StatusCode}
	}

	scanner := bufio.NewScanner(resp.Body)
	var eventType, data string

	for scanner.Scan() {
		select {
		case <-ss.ctx.Done():
			return nil
		default:
		}

		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
		} else if line == "" {
			// Empty line = end of event.
			if eventType != "" && data != "" {
				ss.handleEvent(eventType, data)
			}
			eventType = ""
			data = ""
		}
	}
	return scanner.Err()
}

// handleEvent processes a single SSE event based on its type.
func (ss *streamSource) handleEvent(eventType, data string) {
	switch eventType {
	case "flag.created", "flag.updated":
		var evt streamEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			return
		}
		if evt.Key == "" {
			return
		}
		flag, err := ss.hc.getFlag(evt.Key)
		if err != nil {
			return
		}
		ss.store.setFlag(*flag)
		if ss.onUpdate != nil {
			ss.onUpdate(evt.Key)
		}

	case "flag.deleted":
		var evt streamEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			return
		}
		if evt.Key == "" {
			return
		}
		ss.store.removeFlag(evt.Key)
		if ss.onUpdate != nil {
			ss.onUpdate(evt.Key)
		}

	case "segment.updated":
		resp, err := ss.hc.getFlags()
		if err != nil {
			return
		}
		ss.store.setAll(resp.Flags, resp.Segments)
		if ss.onUpdate != nil {
			ss.onUpdate("")
		}
	}
}

// stop cancels the SSE connection and stops the reconnection loop.
func (ss *streamSource) stop() {
	ss.cancel()
}
