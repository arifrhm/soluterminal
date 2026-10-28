package main

// The hosted services this daemon uses when nothing else is configured.
//
// The product's values are set in hosted_cloud_defaults.go (public since
// 2026-10-27; -ldflags "-X main.hostedCloud=off" leaves them empty). Empty
// values keep entitlements, install check-in and the account proxy off unless
// SOLU_CLOUD_URL is set or the machine is paired; a builder can also set them
// with -ldflags "-X main.defaultCloudHTTPBase=https://…".
var (
	// defaultCloudHTTPBase is the hosted cloud's API origin.
	defaultCloudHTTPBase string
	// publicAppURL is the same cloud's user-facing app domain.
	publicAppURL string
)
