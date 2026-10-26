//go:build !linux

package main

func dropAmbientCaps() error { return nil }
