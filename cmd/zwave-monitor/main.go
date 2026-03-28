package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/VictoriaMetrics/metrics"
	"github.com/gorilla/websocket"
)

type nodeInfo struct {
	NodeID           int    `json:"nodeId"`
	Name             string `json:"name"`
	Location         string `json:"location"`
	Status           int    `json:"status"`
	IsControllerNode bool   `json:"isControllerNode"`
}

func (n nodeInfo) displayName() string {
	var parts []string
	if n.Location != "" {
		parts = append(parts, n.Location)
	}
	if n.Name != "" {
		parts = append(parts, n.Name)
	}
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	return fmt.Sprintf("Node %d", n.NodeID)
}

func nodeMetricName(nodeID int, name string) string {
	return fmt.Sprintf(`zwave_device_up{node_id="%d", name=%q}`, nodeID, name)
}

func statusName(status int) string {
	switch status {
	case 0:
		return "unknown"
	case 1:
		return "asleep"
	case 2:
		return "awake"
	case 3:
		return "dead"
	case 4:
		return "alive"
	default:
		return fmt.Sprintf("status(%d)", status)
	}
}

func isUp(status int) bool {
	return status == 1 || status == 2 || status == 4 // asleep, awake, alive
}

type monitor struct {
	mu    sync.Mutex
	nodes map[int]*nodeInfo
}

func (m *monitor) updateFromNodes(nodes []nodeInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Unregister old metrics
	for _, n := range m.nodes {
		metrics.UnregisterMetric(nodeMetricName(n.NodeID, n.displayName()))
	}

	m.nodes = make(map[int]*nodeInfo)
	for i := range nodes {
		n := &nodes[i]
		if n.IsControllerNode {
			continue
		}
		m.nodes[n.NodeID] = n
		var value float64
		if isUp(n.Status) {
			value = 1
		}
		metrics.GetOrCreateGauge(nodeMetricName(n.NodeID, n.displayName()), nil).Set(value)
		log.Printf("node %s (id=%d): %s", n.displayName(), n.NodeID, statusName(n.Status))
	}
}

func (m *monitor) handleEvent(event map[string]any) {
	source, _ := event["source"].(string)
	if source != "node" {
		return
	}

	eventName, _ := event["event"].(string)
	nodeIDFloat, ok := event["nodeId"].(float64)
	if !ok {
		return
	}
	nodeID := int(nodeIDFloat)

	var newStatus int
	switch eventName {
	case "alive":
		newStatus = 4
	case "dead":
		newStatus = 3
	case "sleep":
		newStatus = 1
	case "wake up":
		newStatus = 2
	default:
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	node, exists := m.nodes[nodeID]
	if !exists {
		return
	}

	oldStatus := node.Status
	if oldStatus == newStatus {
		return
	}

	node.Status = newStatus
	name := node.displayName()

	var value float64
	if isUp(newStatus) {
		value = 1
	}
	metrics.GetOrCreateGauge(nodeMetricName(nodeID, name), nil).Set(value)

	log.Printf("node %s (id=%d): %s -> %s", name, nodeID, statusName(oldStatus), statusName(newStatus))
}

func connect(wsURL string) ([]nodeInfo, *websocket.Conn, error) {
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("dial: %w", err)
	}
	c.SetReadLimit(10 * 1024 * 1024)

	// Handshake
	_, data, err := c.ReadMessage()
	if err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("read handshake: %w", err)
	}
	var handshake struct {
		MaxSchemaVersion int `json:"maxSchemaVersion"`
	}
	if err := json.Unmarshal(data, &handshake); err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("parse handshake: %w", err)
	}

	// set_api_schema
	if err := c.WriteJSON(map[string]any{
		"command":       "set_api_schema",
		"messageId":     1,
		"schemaVersion": handshake.MaxSchemaVersion,
	}); err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("write set_api_schema: %w", err)
	}
	_, data, err = c.ReadMessage()
	if err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("read set_api_schema response: %w", err)
	}
	var schemaResp struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(data, &schemaResp); err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("parse set_api_schema response: %w", err)
	}
	if !schemaResp.Success {
		c.Close()
		return nil, nil, fmt.Errorf("set_api_schema failed")
	}

	// start_listening
	if err := c.WriteJSON(map[string]any{
		"command":   "start_listening",
		"messageId": 2,
	}); err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("write start_listening: %w", err)
	}
	_, data, err = c.ReadMessage()
	if err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("read start_listening response: %w", err)
	}

	var resp struct {
		Success bool `json:"success"`
		Result  struct {
			State struct {
				Nodes []nodeInfo `json:"nodes"`
			} `json:"state"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("parse start_listening: %w", err)
	}
	if !resp.Success {
		c.Close()
		return nil, nil, fmt.Errorf("start_listening failed")
	}

	return resp.Result.State.Nodes, c, nil
}

func runMonitor(wsURL string, m *monitor) {
	for {
		nodes, conn, err := connect(wsURL)
		if err != nil {
			log.Printf("connection error: %v, retrying in 10s", err)
			time.Sleep(10 * time.Second)
			continue
		}

		m.updateFromNodes(nodes)
		log.Printf("connected, monitoring %d nodes", len(m.nodes))

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				log.Printf("read error: %v, reconnecting", err)
				conn.Close()
				break
			}

			var msg struct {
				Type  string         `json:"type"`
				Event map[string]any `json:"event"`
			}
			if err := json.Unmarshal(data, &msg); err != nil {
				log.Printf("parse error: %v", err)
				continue
			}

			if msg.Type == "event" && msg.Event != nil {
				m.handleEvent(msg.Event)
			}
		}

		time.Sleep(5 * time.Second)
	}
}

func main() {
	zwaveAddr := flag.String("zwavejs", "ws://localhost:3000", "Z-Wave JS WebSocket API address")
	metricsPort := flag.Int("port", 9098, "Prometheus metrics port")
	flag.Parse()

	m := &monitor{
		nodes: make(map[int]*nodeInfo),
	}

	go func() {
		http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
			metrics.WritePrometheus(w, false)
		})
		log.Printf("serving metrics on :%d", *metricsPort)
		log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *metricsPort), nil))
	}()

	runMonitor(*zwaveAddr, m)
}
