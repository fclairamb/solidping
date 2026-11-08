//go:build !windows && !darwin

package main

import "context"

func serviceInstall(context.Context, string, string) error { return errServiceUnsupported }

func serviceUninstall(context.Context, string) error { return errServiceUnsupported }

func serviceStart(context.Context, string) error { return errServiceUnsupported }

func serviceStop(context.Context, string) error { return errServiceUnsupported }

func serviceRun(context.Context, string, string) error { return errServiceUnsupported }
