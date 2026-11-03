package cosmostlm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// Errors returned by the client.
var (
	ErrConnection = errors.New("cosmostlm: connection failed")
	ErrAuth       = errors.New("cosmostlm: authentication failed")
	ErrRPC        = errors.New("cosmostlm: json-rpc error")
	ErrRequest    = errors.New("cosmostlm: request failed")
)

// APIPaths are the JSON-RPC endpoints, in probe order. OpenC3 COSMOS Core
// serves /openc3-api/api; Enterprise and ingress-proxied deployments serve
// /__openc3_api__/api.
var APIPaths = []string{"/openc3-api/api", "/__openc3_api__/api"}

// Config configures a Client. Zero values fall back to environment variables,
// then to the documented OpenC3 COSMOS defaults.
type Config struct {
	BaseURL    string
	Scope      string
	Password   string
	AuthMode   string // "keyless" (default), "core", "enterprise"
	APIPath    string
	HTTPClient *http.Client
	Timeout    time.Duration
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Client is a keyless OpenC3 COSMOS v5 JSON-RPC client.
type Client struct {
	baseURL  string
	scope    string
	password string
	authMode string
	apiPath  string
	http     *http.Client
	timeout  time.Duration
	token    string
	reqID    atomic.Int64
}

// NewClient builds a client from cfg, filling gaps from the environment.
func NewClient(cfg Config) *Client {
	c := &Client{
		baseURL:  strings.TrimRight(firstNonEmpty(cfg.BaseURL, os.Getenv("COSMOS_URL"), "http://localhost:2900"), "/"),
		scope:    strings.ToUpper(firstNonEmpty(cfg.Scope, os.Getenv("COSMOS_SCOPE"), "DEFAULT")),
		password: firstNonEmpty(cfg.Password, os.Getenv("COSMOS_PASSWORD")),
		authMode: strings.ToLower(firstNonEmpty(cfg.AuthMode, os.Getenv("COSMOS_AUTH_MODE"), "keyless")),
		apiPath:  cfg.APIPath,
		http:     cfg.HTTPClient,
		timeout:  cfg.Timeout,
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 15 * time.Second}
	}
	if c.timeout == 0 {
		c.timeout = 15 * time.Second
	}
	return c
}

// BaseURL is the resolved COSMOS base URL.
func (c *Client) BaseURL() string { return c.baseURL }

// Scope is the resolved COSMOS scope.
func (c *Client) Scope() string { return c.scope }

// Login authenticates. Keyless mode is a no-op.
func (c *Client) Login(ctx context.Context) error {
	if c.authMode == "" || c.authMode == "keyless" {
		return nil
	}
	if c.password == "" {
		return fmt.Errorf("%w: auth mode %q requires COSMOS_PASSWORD", ErrAuth, c.authMode)
	}
	body, _ := json.Marshal(map[string]string{"password": c.password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/openc3-api/auth/verify", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrConnection, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%w: HTTP %d", ErrAuth, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: auth/verify HTTP %d", ErrRequest, resp.StatusCode)
	}
	token := extractToken(raw)
	if token == "" {
		return fmt.Errorf("%w: no token in auth/verify response", ErrAuth)
	}
	c.token = token
	return nil
}

func extractToken(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}
	var s string
	if json.Unmarshal([]byte(trimmed), &s) == nil {
		return s
	}
	var doc map[string]any
	if json.Unmarshal([]byte(trimmed), &doc) == nil {
		for _, k := range []string{"token", "access_token", "password", "Authorization"} {
			if v, ok := doc[k].(string); ok && v != "" {
				return v
			}
		}
	}
	return strings.Trim(trimmed, `"`)
}

// RPC issues one JSON-RPC 2.0 call and unmarshals the result into out.
func (c *Client) RPC(ctx context.Context, out any, method string, params []any, keyword map[string]any) error {
	if c.token == "" && c.authMode != "" && c.authMode != "keyless" {
		if err := c.Login(ctx); err != nil {
			return err
		}
	}
	if params == nil {
		params = []any{}
	}
	if keyword == nil {
		keyword = map[string]any{}
	}
	if _, ok := keyword["scope"]; !ok && c.scope != "" {
		keyword["scope"] = c.scope
	}
	payload := map[string]any{
		"jsonrpc":        "2.0",
		"method":         method,
		"params":         params,
		"id":             c.reqID.Add(1),
		"keyword_params": keyword,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	paths := APIPaths
	if c.apiPath != "" {
		paths = []string{c.apiPath}
	}
	var lastErr error
	for _, path := range paths {
		status, raw, err := c.post(ctx, path, body)
		if err != nil {
			lastErr = err
			continue
		}
		if status == http.StatusNotFound && c.apiPath == "" {
			lastErr = fmt.Errorf("%w: %s returned 404", ErrRequest, path)
			continue
		}
		c.apiPath = path
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			c.token = ""
			return fmt.Errorf("%w: HTTP %d", ErrAuth, status)
		}
		if status != http.StatusOK {
			return fmt.Errorf("%w: HTTP %d: %s", ErrRequest, status, truncate(raw, 300))
		}
		var env struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return fmt.Errorf("%w: malformed JSON-RPC response: %v", ErrRequest, err)
		}
		if len(env.Error) > 0 && string(env.Error) != "null" {
			return fmt.Errorf("%w: %s: %s", ErrRPC, method, truncate(env.Error, 300))
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(env.Result, out)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%w: no API path succeeded", ErrConnection)
	}
	return lastErr
}

func (c *Client) post(ctx context.Context, path string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %s: %v", ErrConnection, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

// TLM reads one point. valueType is RAW, CONVERTED, or FORMATTED.
func (c *Client) TLM(ctx context.Context, target, packet, item string, valueType ValueType) (json.RawMessage, error) {
	method := "tlm"
	switch valueType {
	case ValueRaw:
		method = "tlm_raw"
	case ValueFormatted:
		method = "tlm_formatted"
	}
	var out json.RawMessage
	err := c.RPC(ctx, &out, method,
		[]any{strings.ToUpper(target), strings.ToUpper(packet), strings.ToUpper(item)},
		map[string]any{"type": string(valueType)})
	return out, err
}

// TLMFloat reads one point as a float64.
func (c *Client) TLMFloat(ctx context.Context, target, packet, item string, vt ValueType) (float64, error) {
	raw, err := c.TLM(ctx, target, packet, item, vt)
	if err != nil {
		return 0, err
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, fmt.Errorf("cosmostlm: %s %s %s is not numeric: %s", target, packet, item, raw)
	}
	return f, nil
}

// TLMRow is one [ITEM, VALUE, LIMITS_STATE] entry from get_tlm_packet.
type TLMRow struct {
	Item  string      `json:"item"`
	Value float64     `json:"value"`
	State LimitsState `json:"state"`
}

// GetTLMPacket returns every point of a packet with its COSMOS limits state.
func (c *Client) GetTLMPacket(ctx context.Context, target, packet string, vt ValueType) ([]TLMRow, error) {
	var rows [][]json.RawMessage
	if err := c.RPC(ctx, &rows, "get_tlm_packet",
		[]any{strings.ToUpper(target), strings.ToUpper(packet)},
		map[string]any{"type": string(vt)}); err != nil {
		return nil, err
	}
	out := make([]TLMRow, 0, len(rows))
	for _, row := range rows {
		if len(row) < 3 {
			continue
		}
		var item string
		var value float64
		var state string
		_ = json.Unmarshal(row[0], &item)
		_ = json.Unmarshal(row[1], &value)
		_ = json.Unmarshal(row[2], &state)
		out = append(out, TLMRow{Item: item, Value: value, State: ParseLimitsState(state)})
	}
	return out, nil
}

// GetTLMCnt returns the receive count for a packet.
func (c *Client) GetTLMCnt(ctx context.Context, target, packet string) (int64, error) {
	var n int64
	err := c.RPC(ctx, &n, "get_tlm_cnt",
		[]any{strings.ToUpper(target), strings.ToUpper(packet)}, nil)
	return n, err
}

// GetOutOfLimits lists every point currently out of limits:
// [TARGET, PACKET, ITEM, LIMITS_STATE].
func (c *Client) GetOutOfLimits(ctx context.Context) ([][4]string, error) {
	var rows [][]string
	if err := c.RPC(ctx, &rows, "get_out_of_limits", nil, nil); err != nil {
		return nil, err
	}
	out := make([][4]string, 0, len(rows))
	for _, r := range rows {
		if len(r) < 4 {
			continue
		}
		out = append(out, [4]string{r[0], r[1], r[2], r[3]})
	}
	return out, nil
}

// GetOverallLimitsState returns GREEN, YELLOW, or RED.
func (c *Client) GetOverallLimitsState(ctx context.Context) (string, error) {
	var s string
	err := c.RPC(ctx, &s, "get_overall_limits_state", nil, nil)
	return s, err
}

// Cmd sends a COSMOS command string. check is ENABLED or NO_CHECK.
func (c *Client) Cmd(ctx context.Context, commandString, check string) (json.RawMessage, error) {
	method := "cmd"
	if strings.EqualFold(check, "NO_CHECK") {
		method = "cmd_no_checks"
	}
	var out json.RawMessage
	err := c.RPC(ctx, &out, method, []any{commandString}, nil)
	return out, err
}

// Health reports whether the server answers a trivial RPC.
func (c *Client) Health(ctx context.Context) bool {
	var out json.RawMessage
	return c.RPC(ctx, &out, "get_server_status", nil, nil) == nil
}

// StreamTopic builds the OpenC3 StreamingApi topic SCOPE__DECOM__{TARGET}__PACKET.
func (c *Client) StreamTopic(target, packet string) string {
	return fmt.Sprintf("%s__DECOM__{%s}__%s", c.scope, strings.ToUpper(target), strings.ToUpper(packet))
}

// Subscribe consumes the decommutated telemetry stream for one packet and
// sends each typed frame on ch until ctx is cancelled or the stream ends.
func (c *Client) Subscribe(ctx context.Context, target, packet string, ch chan<- TelemetryFrame) error {
	defer close(ch)
	u := fmt.Sprintf("%s/openc3-api/stream?%s", c.baseURL, url.Values{
		"topic": {c.StreamTopic(target, packet)},
		"scope": {c.scope},
	}.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", c.token)
	}
	streamClient := &http.Client{Timeout: 0}
	resp, err := streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: stream: %v", ErrConnection, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: stream HTTP %d", ErrRequest, resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] == ':' {
			continue
		}
		var env streamEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			continue
		}
		var frame TelemetryFrame
		if err := json.Unmarshal(env.Frame, &frame); err != nil {
			continue
		}
		frame.LocalReceived = time.Now()
		select {
		case ch <- frame:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return scanner.Err()
}
