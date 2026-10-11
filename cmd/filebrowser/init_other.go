//go:build !linux

package main

// runAsInit is only needed on Linux, where the media sandbox forks helpers.
func runAsInit() {}
