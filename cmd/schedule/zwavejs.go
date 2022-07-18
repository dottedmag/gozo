package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ridge/must/v2"
)

// TODO (dottedmag): This code is quite messy

type request struct {
	id      int
	command string
	params  map[string]any
}

type response struct {
	params map[string]any
}

type conn struct {
	c        *websocket.Conn
	mu       sync.Mutex
	nextID   int
	handlers map[int]chan<- response

	reqs chan request
}

func newConn(url string) (*conn, error) {
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return nil, err
	}

	// Handshake

	var handshake struct {
		MaxSchemaVersion int
	}
	_, data, err := c.ReadMessage()
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &handshake); err != nil {
		return nil, err
	}

	conn := &conn{
		c:        c,
		handlers: map[int]chan<- response{},
		reqs:     make(chan request, 100),
	}

	go func() {
		must.OK(conn.runWrite())
	}()
	go func() {
		must.OK(conn.runRead())
	}()

	resp, err := conn.call("set_api_schema", map[string]any{"schemaVersion": handshake.MaxSchemaVersion})
	if err != nil {
		return nil, err
	}
	if resp.params["success"] == nil || !resp.params["success"].(bool) {
		return nil, fmt.Errorf("failed to set API schema")
	}

	return conn, nil
}

func (c *conn) runWrite() error {
	for {
		req := <-c.reqs
		bb := map[string]any{}
		for k, v := range req.params {
			bb[k] = v
		}
		bb["command"] = req.command
		bb["messageId"] = req.id

		data := must.OK1(json.Marshal(bb))

		if err := c.c.WriteMessage(websocket.TextMessage, data); err != nil {
			return err
		}
	}
}

func (c *conn) runRead() error {
	for {
		_, data, err := c.c.ReadMessage()
		if err != nil {
			return err
		}

		var msg struct {
			Type      string
			MessageID int
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}

		switch msg.Type {
		case "result":
			c.mu.Lock()
			resCh := c.handlers[msg.MessageID]
			c.mu.Unlock()

			if resCh == nil {
				// Request timed out and the handler was removed
				continue
			}

			var resp map[string]any
			must.OK(json.Unmarshal(data, &resp))
			resCh <- response{params: resp}
		case "event":
			// ignored
		default:
			panic(fmt.Errorf("unexpected message %#v", msg))
		}
	}
}

func (c *conn) call(command string, params map[string]any) (response, error) {
	resCh := make(chan response, 1)

	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.handlers[id] = resCh
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.handlers, id)
		c.mu.Unlock()
	}()

	c.reqs <- request{id: id, command: command, params: params}

	select {
	case res := <-resCh:
		return res, nil
	case <-time.After(10 * time.Second):
		return response{}, errors.New("timed out waiting for response")
	}
}
