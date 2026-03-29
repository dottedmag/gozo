package main

import (
	"testing"
	"time"
)

func TestExpectedState(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}

	n := node{
		description: "test",
		schedule: []scheduleEvent{
			{hour: 6, min: 0, sec: 0, state: on},
			{hour: 22, min: 0, sec: 0, state: off},
		},
	}

	tests := []struct {
		name     string
		now      time.Time
		expected state
	}{
		{
			name:     "before first event",
			now:      time.Date(2026, 3, 15, 5, 0, 0, 0, loc),
			expected: off,
		},
		{
			name:     "after first event",
			now:      time.Date(2026, 3, 15, 12, 0, 0, 0, loc),
			expected: on,
		},
		{
			name:     "after second event",
			now:      time.Date(2026, 3, 15, 23, 0, 0, 0, loc),
			expected: off,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expectedState(n, tt.now, loc)
			if got != tt.expected {
				t.Errorf("expectedState at %v = %v, want %v", tt.now, got, tt.expected)
			}
		})
	}
}
