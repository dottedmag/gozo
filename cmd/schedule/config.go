package main

import (
	"fmt"
	"sort"
	"time"
)

type config struct {
	ZWaveJSAPIEndpoint string       `toml:"zwavejs_api_endpoint"`
	Nodes              []configNode `toml:"node"`
}

type configNode struct {
	ID          int
	Description string
	Schedule    []configScheduleEvent
}

type configScheduleEvent struct {
	At string
	On bool
}

func parseConfig(c config) (map[int]node, error) {
	out := map[int]node{}

	for _, cn := range c.Nodes {
		if _, ok := out[cn.ID]; ok {
			return nil, fmt.Errorf("node %d is present multiple times in config", cn.ID)
		}
		var events []scheduleEvent
		for _, cev := range cn.Schedule {
			t, err := time.Parse("15:04:05", cev.At)
			if err != nil {
				return nil, fmt.Errorf("failed to parse time %q: %v", cev.At, err)
			}
			var state state
			if cev.On {
				state = on
			} else {
				state = off
			}
			events = append(events, scheduleEvent{at: t, state: state})
		}
		sort.Slice(events, func(i, j int) bool {
			return events[i].at.Before(events[j].at)
		})

		out[cn.ID] = node{
			description: cn.Description,
			schedule:    events,
		}
	}

	return out, nil
}
