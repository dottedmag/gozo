package main

import (
	"testing"
	"time"
)

func TestNsOfDay(t *testing.T) {
	tests := map[int64]time.Time{
		0:             time.Date(0, time.January, 1, 0, 0, 0, 0, time.UTC),
		1:             time.Date(0, time.January, 1, 0, 0, 0, 1, time.UTC),
		1000000000:    time.Date(0, time.January, 1, 0, 0, 1, 0, time.UTC),
		60000000000:   time.Date(0, time.January, 1, 0, 1, 0, 0, time.UTC),
		3600000000000: time.Date(0, time.January, 1, 1, 0, 0, 0, time.UTC),
		3660000000000: time.Date(1999, time.December, 30, 1, 1, 0, 0, time.UTC),
		3661000000000: time.Date(1999, time.December, 30, 1, 1, 1, 0, time.UTC),
		3661000000001: time.Date(1999, time.December, 30, 1, 1, 1, 1, time.UTC),
	}
	for ns, tm := range tests {
		if nsOfDay(tm) != ns {
			t.Fail()
		}
	}
}
