package main

import (
	"log"
	"os"
	"time"

	"github.com/dottedmag/gozo"
	"github.com/pelletier/go-toml/v2"
)

type node struct {
	description string
	schedule    []scheduleEvent // sorted by time
}

type scheduleEvent struct {
	at    time.Time
	state state
}

func nsOfDay(t time.Time) int64 {
	return int64(t.Nanosecond()) +
		1000000000*(int64(t.Second())+
			60*(int64(t.Minute())+
				60*int64(t.Hour())))
}

func laterWithinDay(t1, t2 time.Time) bool {
	return nsOfDay(t1) > nsOfDay(t2)
}

func expectedState(n node, time time.Time) state {
	// if time is earlier than the first event in schedule, then
	// the right state is the last one (previous day) in schedule
	lastState := n.schedule[len(n.schedule)-1].state

	for _, event := range n.schedule {
		if laterWithinDay(event.at, time) {
			return lastState
		}
		lastState = event.state
	}
	return lastState
}

type state string

const (
	unknown state = "unknown"
	off     state = "off"
	on      state = "on"
)

func main() {
	log.SetFlags(log.LUTC)

	if len(os.Args) != 2 {
		log.Printf("Usage: schedule <config-file>")
		os.Exit(2)
	}

	fh, err := os.Open(os.Args[1])
	if err != nil {
		log.Printf("FATAL: Failed to open config file %s: %v", os.Args[1], err)
		os.Exit(1)
	}

	var config config
	dec := toml.NewDecoder(fh)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&config); err != nil {
		log.Printf("FATAL: Failed to parse config file %s: %v", os.Args[1], err)
		os.Exit(1)
	}

	nodes, err := parseConfig(config)
	if err != nil {
		log.Printf("FATAL: Failed to parse config file %s: %v", os.Args[1], err)
		os.Exit(1)
	}

	c, err := gozo.NewConn(config.ZWaveJSAPIEndpoint, func(m map[string]interface{}) {})
	if err != nil {
		// TODO (dottedmag): Handle zwave-js API endpoint reconnections
		log.Printf("FATAL: Failed to connect to zwave-js API endpoint %s: %v", config.ZWaveJSAPIEndpoint, err)
		os.Exit(1)
	}

	nodesCurrentStates := map[int]state{}
	for id, node := range nodes {
		log.Printf("INFO: Servicing node %d (%s)", id, node.description)
		nodesCurrentStates[id] = unknown
	}

	for {
		var anyFailed bool

		for id, node := range nodes {
			expected := expectedState(node, time.Now())
			if nodesCurrentStates[id] == expected {
				continue
			}

			targetValue := 0
			if expected == on {
				targetValue = 1
			}

			// TODO (dottedmag): Recongnize manual manipulations, and back off
			resp, err := c.Call("endpoint.invoke_cc_api", map[string]any{
				"nodeId":       id,
				"endpoint":     1,
				"commandClass": 0x40, // thermostat mode
				"methodName":   "set",
				"args":         []int{targetValue},
			})

			if err != nil {
				log.Printf("ERR: Failed to transition %d (%s) %v->%v: %v", id, node.description, nodesCurrentStates[id], expected, err)
				anyFailed = true
				continue
			}

			// TODO (dottedmag): Recongnize "node is offline", and use different scheduling algorithm
			// (offline nodes are likely to stay offline for a while, as they are probably just unplugged)
			if resp["success"] == nil || !resp["success"].(bool) {
				log.Printf("ERR: Failed to transition %d (%s) %v->%v: %#v", id, node.description, nodesCurrentStates[id], expected, resp)
				anyFailed = true
				continue
			}

			log.Printf("INFO: Transitioned %d (%s) %v->%v", id, node.description, nodesCurrentStates[id], expected)
			nodesCurrentStates[id] = expected
		}

		if anyFailed {
			time.Sleep(10 * time.Second)
		} else {
			// TODO (dottedmag): Increase precision of scheduling
			time.Sleep(5 * time.Minute)
		}
	}
}
