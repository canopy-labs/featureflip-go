package featureflip

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

const sdkVersion = "0.1.0"

// httpClient wraps stdlib net/http for communication with the evaluation API.
type httpClient struct {
	client  *http.Client
	baseURL string
	sdkKey  string
}

// newHTTPClient creates a new httpClient with the given SDK key and config.
func newHTTPClient(sdkKey string, cfg config) *httpClient {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: cfg.connectTimeout,
		}).DialContext,
		ResponseHeaderTimeout: cfg.readTimeout,
	}

	return &httpClient{
		client: &http.Client{
			Transport: transport,
			Timeout:   cfg.connectTimeout + cfg.readTimeout,
		},
		baseURL: cfg.baseURL,
		sdkKey:  sdkKey,
	}
}

// setHeaders sets the standard SDK headers on a request.
func (h *httpClient) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", h.sdkKey)
	req.Header.Set("User-Agent", "featureflip-go/"+sdkVersion)
}

// getFlags fetches all flag and segment configurations from the evaluation API.
func (h *httpClient) getFlags() (*getFlagsResponse, error) {
	req, err := http.NewRequest(http.MethodGet, h.baseURL+"/v1/sdk/flags", nil)
	if err != nil {
		return nil, fmt.Errorf("featureflip: create request: %w", err)
	}
	h.setHeaders(req)

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("featureflip: get flags: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("featureflip: get flags: unexpected status %d", resp.StatusCode)
	}

	var result getFlagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("featureflip: decode flags response: %w", err)
	}

	return &result, nil
}

// getFlag fetches a single flag configuration by key from the evaluation API.
func (h *httpClient) getFlag(key string) (*flagDTO, error) {
	reqURL := h.baseURL + "/v1/sdk/flags/" + url.PathEscape(key)
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("featureflip: create request: %w", err)
	}
	h.setHeaders(req)

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("featureflip: get flag %q: %w", key, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("featureflip: get flag %q: unexpected status %d", key, resp.StatusCode)
	}

	var result flagDTO
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("featureflip: decode flag response: %w", err)
	}

	return &result, nil
}

// postEvents sends a batch of SDK events to the evaluation API.
func (h *httpClient) postEvents(events []sdkEvent) error {
	body, err := json.Marshal(recordEventsRequest{Events: events})
	if err != nil {
		return fmt.Errorf("featureflip: marshal events: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, h.baseURL+"/v1/sdk/events", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("featureflip: create request: %w", err)
	}
	h.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("featureflip: post events: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("featureflip: post events: unexpected status %d", resp.StatusCode)
	}

	return nil
}

// newStreamRequest creates an HTTP request for the SSE stream endpoint.
func (h *httpClient) newStreamRequest() (*http.Request, error) {
	req, err := http.NewRequest(http.MethodGet, h.baseURL+"/v1/sdk/stream", nil)
	if err != nil {
		return nil, fmt.Errorf("featureflip: create stream request: %w", err)
	}
	h.setHeaders(req)
	req.Header.Set("Accept", "text/event-stream")
	return req, nil
}

// streamHTTPClient returns an http.Client configured for SSE streaming.
// It uses the connect timeout for dialing but has no response/overall timeout
// so the connection can remain open indefinitely.
func (h *httpClient) streamHTTPClient(connectTimeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: connectTimeout,
			}).DialContext,
		},
		// No Timeout — SSE connections are long-lived.
	}
}
