package gozo

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

type Conn struct {
	c        *websocket.Conn
	mu       sync.Mutex
	nextID   int
	handlers map[int]chan<- map[string]any

	eventHandler func(map[string]interface{})

	reqs chan request
}

func NewConn(url string, eventHandler func(map[string]interface{})) (*Conn, error) {
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

	conn := &Conn{
		c:            c,
		handlers:     map[int]chan<- map[string]any{},
		reqs:         make(chan request, 100),
		eventHandler: eventHandler,
	}

	go func() {
		must.OK(conn.runWrite())
	}()
	go func() {
		must.OK(conn.runRead())
	}()

	resp, err := conn.Call("set_api_schema", map[string]any{"schemaVersion": handshake.MaxSchemaVersion})
	if err != nil {
		return nil, err
	}
	if resp["success"] == nil || !resp["success"].(bool) {
		return nil, fmt.Errorf("failed to set API schema")
	}

	resp, err = conn.Call("start_listening", nil)
	if err != nil {
		return nil, err
	}
	if resp["success"] == nil || !resp["success"].(bool) {
		return nil, fmt.Errorf("failed to start listening to events")
	}

	return conn, nil
}

func (c *Conn) runWrite() error {
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

func (c *Conn) runRead() error {
	for {
		_, data, err := c.c.ReadMessage()
		if err != nil {
			return err
		}

		var msg struct {
			Type      string
			MessageID int
			Event     map[string]interface{}
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

			//fmt.Printf("%s\n", data)
			var resp map[string]any
			must.OK(json.Unmarshal(data, &resp))
			resCh <- resp
		case "event":
			if c.eventHandler == nil {
				panic(fmt.Errorf("event received with nil eventHandler"))
			} else {
				c.eventHandler(msg.Event)
			}
			// ignored
		default:
			panic(fmt.Errorf("unexpected message %#v", msg))
		}
	}
}

func (c *Conn) Call(command string, params map[string]any) (map[string]any, error) {
	resCh := make(chan map[string]any, 1)

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
		return nil, errors.New("timed out waiting for response")
	}
}
