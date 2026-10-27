//go:build windows

package main

import "syscall"

func previewDetachAttr() *syscall.SysProcAttr { return nil }
