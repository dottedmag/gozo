package main

import (
	"fmt"
	"sort"
	"time"
)

type config struct {
	ZWaveJSAPIEndpoint string       `toml:"zwavejs_api_endpoint"`
	Timezone           string       `toml:"timezone"`
	Nodes              []configNode `toml:"node"`
}

type configNode struct {
	ID          int
	Description string
	Schedule    []configSchedule
}

type configSchedule struct {
	At string
	On bool
}

func parseConfig(c config) (*time.Location, map[int]node, error) {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load timezone %q: %v", c.Timezone, err)
	}

	out := map[int]node{}

	for _, cn := range c.Nodes {
		if _, ok := out[cn.ID]; ok {
			return nil, nil, fmt.Errorf("node %d is present multiple times in config", cn.ID)
		}
		var events []scheduleEvent
		for _, cev := range cn.Schedule {
			t, err := time.Parse("15:04:05", cev.At)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to parse time %q: %v", cev.At, err)
			}
			var state state
			if cev.On {
				state = on
			} else {
				state = off
			}
			events = append(events, scheduleEvent{
				hour:  t.Hour(),
				min:   t.Minute(),
				sec:   t.Second(),
				state: state,
			})
		}
		sort.Slice(events, func(i, j int) bool {
			return events[i].dayOffset() < events[j].dayOffset()
		})

		out[cn.ID] = node{
			description: cn.Description,
			schedule:    events,
		}
	}

	return loc, out, nil
}
