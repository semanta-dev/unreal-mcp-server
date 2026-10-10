// Package livetest holds the live tests (build tag "live"): the real server against
// running Unreal editors on scratch projects. See harness_test.go; scripts/live.sh runs
// them. Without the tag the package is empty, so go test ./... skips it.
package livetest
