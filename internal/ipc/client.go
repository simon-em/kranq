package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/effetmonstre/forge/internal/exitcode"
	"github.com/effetmonstre/forge/internal/state"
)

type Client struct {
	http *http.Client
}

type RemoteError struct {
	Code int
	Msg  string
}

func (e *RemoteError) Error() string { return e.Msg }

func NewClient(socket string) *Client {
	return &Client{http: &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
	}}
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://forge"+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return &RemoteError{Code: exitcode.Unreachable, Msg: "cannot reach the forge daemon: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var e ErrorBody
		if json.NewDecoder(resp.Body).Decode(&e) == nil && e.Error != "" {
			return &RemoteError{Code: e.Code, Msg: e.Error}
		}
		return &RemoteError{Code: exitcode.InternalError, Msg: fmt.Sprintf("daemon returned %s", resp.Status)}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) Ping(ctx context.Context) error {
	var s Status
	return c.do(ctx, http.MethodGet, "/v1/status", nil, &s)
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	var s Status
	return s, c.do(ctx, http.MethodGet, "/v1/status", nil, &s)
}

func (c *Client) Submit(ctx context.Context, req SubmitRequest) (state.Task, error) {
	var t state.Task
	return t, c.do(ctx, http.MethodPost, "/v1/tasks", req, &t)
}

func (c *Client) List(ctx context.Context) ([]state.Task, error) {
	var l TaskList
	return l.Tasks, c.do(ctx, http.MethodGet, "/v1/tasks", nil, &l)
}

func (c *Client) Get(ctx context.Context, id string) (state.Task, error) {
	var t state.Task
	return t, c.do(ctx, http.MethodGet, "/v1/tasks/"+id, nil, &t)
}

func (c *Client) Cancel(ctx context.Context, id string) (state.Task, error) {
	var t state.Task
	return t, c.do(ctx, http.MethodPost, "/v1/tasks/"+id+"/cancel", nil, &t)
}

func (c *Client) Logs(ctx context.Context, id string, follow bool, w io.Writer) error {
	path := "/v1/tasks/" + id + "/logs"
	if follow {
		path += "?follow=1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://forge"+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return &RemoteError{Code: exitcode.Unreachable, Msg: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return &RemoteError{Code: exitcode.NoSuchFile, Msg: "no such task"}
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func (c *Client) Artifacts(ctx context.Context, id string, w io.Writer) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://forge/v1/tasks/"+id+"/artifacts", nil)
	if err != nil {
		return false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false, &RemoteError{Code: exitcode.Unreachable, Msg: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode >= 400 {
		return false, &RemoteError{Code: exitcode.InternalError, Msg: resp.Status}
	}
	_, err = io.Copy(w, resp.Body)
	return err == nil, err
}

func (c *Client) WaitReady(ctx context.Context, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		if err := c.Ping(ctx); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return &RemoteError{Code: exitcode.Unreachable, Msg: "the daemon did not come up in " + d.String()}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
