//go:build !unix

package main

func isRoot() bool { return false }
