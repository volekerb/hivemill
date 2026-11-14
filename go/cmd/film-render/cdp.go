package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// cdpClient is a minimal Chrome DevTools Protocol client over the --remote-debugging-pipe transport:
// JSON messages, each terminated by a NUL byte, in both directions.
type cdpClient struct {
	w       io.Writer
	wmu     sync.Mutex
	timeout time.Duration

	mu      sync.Mutex
	next    int
	pending map[int]chan cdpReply
	closed  error
}

type cdpReply struct {
	result json.RawMessage
	err    error
}

type cdpMessage struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type cdpRequest struct {
	ID        int    `json:"id"`
	Method    string `json:"method"`
	Params    any    `json:"params,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

// newCDP starts the read loop on r; w receives the requests.
func newCDP(w io.Writer, r io.Reader, timeout time.Duration) *cdpClient {
	c := &cdpClient{w: w, timeout: timeout, pending: map[int]chan cdpReply{}}
	go c.readLoop(r)
	return c
}

func (c *cdpClient) readLoop(r io.Reader) {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		raw, err := br.ReadBytes(0)
		if len(raw) > 1 {
			var m cdpMessage
			if jerr := json.Unmarshal(raw[:len(raw)-1], &m); jerr == nil && m.ID > 0 {
				rep := cdpReply{result: m.Result}
				if m.Error != nil {
					rep.err = fmt.Errorf("cdp error %d: %s", m.Error.Code, m.Error.Message)
				}
				c.mu.Lock()
				ch := c.pending[m.ID]
				delete(c.pending, m.ID)
				c.mu.Unlock()
				if ch != nil {
					ch <- rep
				}
			}
		}
		if err != nil {
			c.mu.Lock()
			c.closed = fmt.Errorf("browser connection closed: %v", err)
			for id, ch := range c.pending {
				ch <- cdpReply{err: c.closed}
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}
	}
}

// call sends one command (session "" = browser level) and waits for its reply, the per-call timeout or ctx.
func (c *cdpClient) call(ctx context.Context, session, method string, params any) (json.RawMessage, error) {
	ch := make(chan cdpReply, 1)
	c.mu.Lock()
	if c.closed != nil {
		err := c.closed
		c.mu.Unlock()
		return nil, err
	}
	c.next++
	id := c.next
	c.pending[id] = ch
	c.mu.Unlock()

	data, err := json.Marshal(cdpRequest{ID: id, Method: method, Params: params, SessionID: session})
	if err == nil {
		c.wmu.Lock()
		_, err = c.w.Write(append(data, 0))
		c.wmu.Unlock()
	}
	if err != nil {
		c.drop(id)
		return nil, fmt.Errorf("cdp %s: write: %w", method, err)
	}
	tctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	select {
	case rep := <-ch:
		if rep.err != nil {
			return nil, fmt.Errorf("%s: %w", method, rep.err)
		}
		return rep.result, nil
	case <-tctx.Done():
		c.drop(id)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("cdp %s: no reply within %s", method, c.timeout)
	}
}

func (c *cdpClient) drop(id int) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}
