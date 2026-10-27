//go:build !linux && !windows

package main

import "context"

func probeJevProcessIdentity(ctx context.Context, root int) (jevProcessIdentity, error) {
	if err := ctx.Err(); err != nil {
		return jevProcessIdentity{}, err
	}
	return jevProcessIdentity{}, errJevProcessUnsupported
}
