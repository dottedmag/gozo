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
	hour, min, sec int
	state          state
}

func (e scheduleEvent) dayOffset() int {
	return e.sec + 60*(e.min+60*e.hour)
}

func (e scheduleEvent) timeOn(now time.Time, loc *time.Location) time.Time {
	local := now.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), e.hour, e.min, e.sec, 0, loc)
}

func expectedState(n node, now time.Time, loc *time.Location) state {
	// if now is earlier than the first event in schedule, then
	// the right state is the last one (previous day) in schedule
	lastState := n.schedule[len(n.schedule)-1].state

	for _, event := range n.schedule {
		if now.Before(event.timeOn(now, loc)) {
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

	loc, nodes, err := parseConfig(config)
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
			expected := expectedState(node, time.Now(), loc)
			if nodesCurrentStates[id] == expected {
				continue
			}

			var targetValue bool
			if expected == on {
				targetValue = true
			}

			// TODO (dottedmag): Recongnize manual manipulations, and back off
			resp, err := c.Call("endpoint.invoke_cc_api", map[string]any{
				"nodeId":       id,
				"commandClass": 0x25, // binary switch
				"methodName":   "set",
				"args":         []bool{targetValue},
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
