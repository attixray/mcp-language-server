package lsp

import (
	"context"
	"fmt"
	"sort"
	"time"
)

type failureSnapshot struct{ Requests []Activity }

// Activity contains identifiers and timing only, never document contents.
type Activity struct {
	ID      string    `json:"id"`
	Method  string    `json:"method"`
	Phase   string    `json:"phase"`
	Started time.Time `json:"started"`
}

// SetRequestTimeout sets the upper bound for future non-initialization calls.
func (c *Client) SetRequestTimeout(timeout time.Duration) { c.requestTimeout.Store(int64(timeout)) }
func (c *Client) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := time.Duration(c.requestTimeout.Load())
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}
func (c *Client) track(id, method, phase string) {
	c.activityMu.Lock()
	defer c.activityMu.Unlock()
	if c.activity == nil {
		c.activity = make(map[string]Activity)
	}
	a := c.activity[id]
	if a.Started.IsZero() {
		a = Activity{ID: id, Method: method, Started: time.Now()}
	}
	a.Phase = phase
	c.activity[id] = a
}
func (c *Client) untrack(id string) {
	c.activityMu.Lock()
	delete(c.activity, id)
	c.activityMu.Unlock()
}
func (c *Client) Activities() []Activity {
	c.activityMu.Lock()
	defer c.activityMu.Unlock()
	result := make([]Activity, 0, len(c.activity))
	for _, a := range c.activity {
		result = append(result, a)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func (c *Client) Done() <-chan struct{} { return c.done }

// WaitForHandlers joins the receive loop, including synchronous server requests
// such as workspace/applyEdit. Done alone only signals transport shutdown.
func (c *Client) WaitForHandlers(ctx context.Context) error {
	if c.messagesDone == nil {
		return nil // No receive loop (synthetic clients used by transport tests).
	}
	select {
	case <-c.messagesDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *Client) OpenPaths() []string {
	c.openFilesMu.RLock()
	defer c.openFilesMu.RUnlock()
	paths := make([]string, 0, len(c.openFiles))
	for _, file := range c.openFiles {
		paths = append(paths, file.URI.Path())
	}
	sort.Strings(paths)
	return paths
}

// Abort does not acquire any request/file/write lock. It is safe for the
// watchdog to call while another goroutine is blocked holding those locks.
func (c *Client) Abort() {
	c.abortOnce.Do(func() {
		c.failure.Store(&failureSnapshot{Requests: c.Activities()})
		c.doneOnce.Do(func() {
			if c.done != nil {
				close(c.done)
			}
		})
		if c.processTree != nil {
			c.processTree.kill()
		} else if c.Cmd != nil && c.Cmd.Process != nil {
			_ = c.Cmd.Process.Kill()
		}
		if c.stdin != nil {
			_ = c.stdin.Close()
		}
		if c.stdoutPipe != nil {
			_ = c.stdoutPipe.Close()
		}
	})
}
func (c *Client) writeServerResponse(msg *Message) error {
	ctx, cancel := c.bounded(context.Background())
	defer cancel()
	if err := c.writeMessage(ctx, msg); err != nil {
		return fmt.Errorf("server response: %w", err)
	}
	return nil
}

func (c *Client) FailedActivities() []Activity {
	if state := c.failure.Load(); state != nil {
		return append([]Activity(nil), state.Requests...)
	}
	return nil
}
