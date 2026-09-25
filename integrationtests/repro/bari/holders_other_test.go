//go:build !windows

package bari

func lockHolders(string) []string { return nil }
