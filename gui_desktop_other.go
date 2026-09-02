//go:build !windows

package main

func launchDesktopGUI(string) bool { return false }

func shutdownDesktopGUI() {}
