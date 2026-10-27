package main

// The hosted services this daemon uses when nothing else is configured.
//
// Benjamin's 2026-10-25 decision: a public build connects to no hosted cloud
// or relay unless its user configures one. These are empty there:
// entitlements, install check-in and the account proxy stay off unless
// SOLU_CLOUD_URL is set (or the machine is paired), and the ARIA responder
// needs an agent gateway URL. The product build's values live in
// hosted_cloud_defaults.go, which the public source export omits; a builder
// can also set them with -ldflags "-X main.defaultCloudHTTPBase=https://…".
var (
	// defaultCloudHTTPBase is the hosted cloud's API origin.
	defaultCloudHTTPBase string
	// publicAppURL is the same cloud's user-facing app domain.
	publicAppURL string
	// ariaResponderGatewayDefault is the hosted ARIA comms gateway.
	ariaResponderGatewayDefault string
)
