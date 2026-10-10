//go:build live

package livetest

import (
	"fmt"
	"testing"
	"time"
)

// The game's own API places a straight road in a fresh game world: the home city's road
// count rises by exactly the cells placed.
func TestLivePolyWorldPlaceRoad(t *testing.T) {
	l := session(t, "POLYWORLD")
	l.enable("game")
	l.freshPIE()
	roads := func() float64 {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); ; {
			s, isErr := l.raw("game", map[string]any{"op": "snapshot"})
			if !isErr && s["error"] == nil {
				cities, _ := s["cities"].([]any)
				for _, c := range cities {
					if m, _ := c.(map[string]any); m["city_id"] == "Home" {
						return num(m["road_cell_count"])
					}
				}
				t.Fatalf("no Home city in %v", s)
			}
			if time.Now().After(deadline) {
				t.Fatalf("snapshot never ready: %v", s)
			}
			time.Sleep(time.Second) // snapshot_not_ready while the world initialises
		}
	}
	before := roads()
	cells := []any{}
	for y := 50; y < 54; y++ {
		cells = append(cells, map[string]any{"x": 47, "y": y})
	}
	l.call("game_command", map[string]any{"name": "place_road", "request_id": fmt.Sprintf("livetest-road-%d", time.Now().UnixNano()),
		"args": map[string]any{"city_id": "Home", "cells": cells}})
	if after := roads(); after != before+4 {
		t.Fatalf("road cells %v -> %v, want +4", before, after)
	}
}
