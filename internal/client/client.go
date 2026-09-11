// Package client talks to the hub over its Unix socket. It is the only path
// the CLI and the dashboard use; nothing else should build HTTP requests.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/philband/callboard/internal/api"
)

// APIError is a non-2xx response from the hub.
type APIError struct {
	Code int
	Msg  string
}

func (e *APIError) Error() string { return e.Msg }

// IsNotFound reports whether err is a 404 from the hub.
func IsNotFound(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Code == http.StatusNotFound
}

// IsRestarting reports whether err means the hub is not there right now:
// the 503 a long poll gets while the hub drains for a restart, or any
// transport failure (socket gone, connection refused, reset, EOF). Both are
// worth reconnecting on rather than reporting to the user. Context errors
// are the caller's own doing and never count.
func IsRestarting(err error) bool {
	if err == nil {
		return false
	}
	var e *APIError
	if errors.As(err, &e) {
		return e.Code == http.StatusServiceUnavailable && strings.Contains(e.Msg, api.MsgHubRestarting)
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

// Client is a hub connection. It is safe for concurrent use.
type Client struct {
	socket string
	http   *http.Client
}

// New returns a client for the hub socket. It does not connect.
func New(socket string) *Client {
	return &Client{
		socket: socket,
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socket)
				},
				DisableKeepAlives: true,
			},
			// No Timeout: long-poll calls bound themselves via wait/ctx.
		},
	}
}

// Socket returns the socket path this client dials.
func (c *Client) Socket() string { return c.socket }

func (c *Client) do(ctx context.Context, method, path string, q url.Values, in, out any) error {
	u := "http://callboard" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e api.ErrorResponse
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return &APIError{Code: resp.StatusCode, Msg: e.Error}
		}
		return &APIError{Code: resp.StatusCode, Msg: fmt.Sprintf("hub returned %s", resp.Status)}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func (c *Client) Health(ctx context.Context) (api.HealthResponse, error) {
	var out api.HealthResponse
	err := c.do(ctx, http.MethodGet, "/v1/health", nil, nil, &out)
	return out, err
}

// Shutdown asks the hub to stop gracefully. A non-zero ifModTime must match
// the hub's own build or it refuses with 409.
func (c *Client) Shutdown(ctx context.Context, ifModTime time.Time) error {
	return c.do(ctx, http.MethodPost, "/v1/shutdown", nil, api.ShutdownRequest{IfModTime: ifModTime}, nil)
}

func (c *Client) Checkin(ctx context.Context, req api.CheckinRequest) (api.CheckinResponse, error) {
	var out api.CheckinResponse
	err := c.do(ctx, http.MethodPost, "/v1/checkin", nil, req, &out)
	return out, err
}

func (c *Client) Checkout(ctx context.Context, as string) error {
	return c.do(ctx, http.MethodPost, "/v1/checkout", nil, api.CheckoutRequest{As: as}, nil)
}

func (c *Client) Resolve(ctx context.Context, req api.ResolveRequest) (api.Session, error) {
	var out api.ResolveResponse
	err := c.do(ctx, http.MethodPost, "/v1/resolve", nil, req, &out)
	return out.Session, err
}

func (c *Client) Sessions(ctx context.Context, scope string) ([]api.Session, error) {
	q := url.Values{}
	if scope != "" {
		q.Set("scope", scope)
	}
	var out api.SessionsResponse
	err := c.do(ctx, http.MethodGet, "/v1/sessions", q, nil, &out)
	return out.Sessions, err
}

func (c *Client) Send(ctx context.Context, req api.SendRequest) (api.SendResponse, error) {
	var out api.SendResponse
	err := c.do(ctx, http.MethodPost, "/v1/messages", nil, req, &out)
	return out, err
}

// Inbox long-polls the undelivered messages of a session for up to wait.
// Unless peek is set the returned messages are marked delivered.
func (c *Client) Inbox(ctx context.Context, as string, wait time.Duration, peek bool) ([]api.Message, error) {
	q := url.Values{"as": {as}}
	if wait > 0 {
		q.Set("wait", wait.String())
	}
	if peek {
		q.Set("peek", "1")
	}
	var out api.InboxResponse
	err := c.do(ctx, http.MethodGet, "/v1/inbox", q, nil, &out)
	return out.Messages, err
}

// MarkDelivered acknowledges messages obtained with peek once they have
// reached the agent by another channel.
func (c *Client) MarkDelivered(ctx context.Context, as string, ids []string) error {
	return c.do(ctx, http.MethodPost, "/v1/delivered", nil, api.DeliveredRequest{As: as, IDs: ids}, nil)
}

func (c *Client) History(ctx context.Context, as string) ([]api.Message, error) {
	var out api.InboxResponse
	err := c.do(ctx, http.MethodGet, "/v1/history", url.Values{"as": {as}}, nil, &out)
	return out.Messages, err
}

func (c *Client) PostJob(ctx context.Context, req api.PostJobRequest) (api.Job, error) {
	var out api.JobResponse
	err := c.do(ctx, http.MethodPost, "/v1/jobs", nil, req, &out)
	return out.Job, err
}

func (c *Client) Jobs(ctx context.Context, scope, status string) ([]api.Job, error) {
	q := url.Values{}
	if scope != "" {
		q.Set("scope", scope)
	}
	if status != "" {
		q.Set("status", status)
	}
	var out api.JobsResponse
	err := c.do(ctx, http.MethodGet, "/v1/jobs", q, nil, &out)
	return out.Jobs, err
}

func (c *Client) Job(ctx context.Context, id string) (api.Job, error) {
	var out api.JobResponse
	err := c.do(ctx, http.MethodGet, "/v1/jobs/"+url.PathEscape(id), nil, nil, &out)
	return out.Job, err
}

func (c *Client) ClaimJob(ctx context.Context, id, as string) (api.Job, error) {
	return c.jobAction(ctx, id, "claim", api.JobActionRequest{As: as})
}

func (c *Client) DoneJob(ctx context.Context, id, as, result string) (api.Job, error) {
	return c.jobAction(ctx, id, "done", api.JobActionRequest{As: as, Result: result})
}

func (c *Client) FailJob(ctx context.Context, id, as, reason string) (api.Job, error) {
	return c.jobAction(ctx, id, "fail", api.JobActionRequest{As: as, Result: reason})
}

func (c *Client) jobAction(ctx context.Context, id, action string, req api.JobActionRequest) (api.Job, error) {
	var out api.JobResponse
	err := c.do(ctx, http.MethodPost, "/v1/jobs/"+url.PathEscape(id)+"/"+action, nil, req, &out)
	return out.Job, err
}

// Events long-polls the global event stream after seq since.
func (c *Client) Events(ctx context.Context, since uint64, wait time.Duration, scope string) (api.EventsResponse, error) {
	q := url.Values{"since": {strconv.FormatUint(since, 10)}}
	if wait > 0 {
		q.Set("wait", wait.String())
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	var out api.EventsResponse
	err := c.do(ctx, http.MethodGet, "/v1/events", q, nil, &out)
	return out, err
}
