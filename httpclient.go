package featureflip

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

// sdkVersion is reported in the User-Agent. Go modules carry no manifest — the
// git tag is the version — so this is maintained by hand and pinned to
// CHANGELOG.md by tools/check-sdk-versions. Bump both together.
const sdkVersion = "2.10.0"

// productToken matches an RFC 9110 product-token: one or more tchars.
var productToken = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// userAgent is the SDK token, then the configured wrapper's token (#4149). A
// wrapper that is not a valid product token is dropped with a logged warning
// rather than failing construction: identity metadata must never stop the client
// starting.
func userAgent(cfg config) string {
	ua := "featureflip-go/" + sdkVersion
	if cfg.wrapperName == "" {
		return ua
	}
	if !productToken.MatchString(cfg.wrapperName) ||
		(cfg.wrapperVersion != "" && !productToken.MatchString(cfg.wrapperVersion)) {
		log.Printf("[featureflip] ignoring wrapper %q/%q: not a valid User-Agent product token",
			cfg.wrapperName, cfg.wrapperVersion)
		return ua
	}
	if cfg.wrapperVersion == "" {
		return ua + " " + cfg.wrapperName
	}
	return ua + " " + cfg.wrapperName + "/" + cfg.wrapperVersion
}

// httpClient wraps stdlib net/http for communication with the evaluation API.
type httpClient struct {
	client    *http.Client
	baseURL   string
	sdkKey    string
	userAgent string
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
		baseURL:   cfg.baseURL,
		sdkKey:    sdkKey,
		userAgent: userAgent(cfg),
	}
}

// setHeaders sets the standard SDK headers on a request.
func (h *httpClient) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", h.sdkKey)
	req.Header.Set("User-Agent", h.userAgent)
}

// isMalformedPayload reports whether err came from decoding a response body
// rather than from moving bytes — i.e. whether the server broke the wire
// contract, or the network merely hiccupped.
//
// The distinction decides whether a caller announces the failure. A transport
// failure self-heals on the next fetch and stays quiet; a decode failure does
// not, because the server will send the same payload again. Every fetch here
// wraps its decode error with %w, so the concrete json errors stay reachable
// through errors.As.
//
// Deliberately narrow: a truncated body surfaces as io.ErrUnexpectedEOF, which
// is a transport symptom, not a contract violation.
func isMalformedPayload(err error) bool {
	var typeErr *json.UnmarshalTypeError
	var syntaxErr *json.SyntaxError
	return errors.As(err, &typeErr) || errors.As(err, &syntaxErr)
}

// eventSendError reports that the events endpoint answered a flush with a
// non-2xx status.
//
// The status has to survive the return from postEvents so the flush path can
// tell a retryable failure from a permanent one (#2456) — a bare
// fmt.Errorf("... status %d") would force the caller to string-match, which is
// exactly the coupling errors.As exists to avoid.
type eventSendError struct {
	statusCode int
}

func (e *eventSendError) Error() string {
	return fmt.Sprintf("featureflip: post events: unexpected status %d", e.statusCode)
}

// retryable reports whether the same batch could succeed if sent again: any 5xx
// (the production edge answers this endpoint with a 503 at a low constant rate
// — #2456) and 429, where the server is explicitly asking the caller to come
// back later.
func (e *eventSendError) retryable() bool {
	return e.statusCode >= 500 || e.statusCode == 429
}

// isRetryableSendFailure reports whether a failed event flush could plausibly
// succeed if the same batch were sent again.
//
// Anything that reached the wire and came back as an HTTP answer is decided by
// the status. Everything else is treated as transient: a failure out of
// http.Client.Do is a transport fault (DNS, TLS, connection reset) or a
// timeout, and a later flush may well get past it.
//
// The one non-HTTP failure that is NOT transient is an encode failure. A batch
// whose caller-supplied metadata carries a func, a channel or a NaN will fail
// to marshal identically every time, so re-queueing it would pin it at the
// front of the buffer forever and starve every later event — the same reason a
// 401 is dropped rather than retried.
func isRetryableSendFailure(err error) bool {
	var sendErr *eventSendError
	if errors.As(err, &sendErr) {
		return sendErr.retryable()
	}

	var unsupportedType *json.UnsupportedTypeError
	var unsupportedValue *json.UnsupportedValueError
	var marshalerErr *json.MarshalerError
	if errors.As(err, &unsupportedType) ||
		errors.As(err, &unsupportedValue) ||
		errors.As(err, &marshalerErr) {
		return false
	}

	return true
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
		io.Copy(io.Discard, resp.Body)
		return &eventSendError{statusCode: resp.StatusCode}
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
