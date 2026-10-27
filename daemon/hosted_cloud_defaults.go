package main

// The SoluTerminal product's hosted cloud (hosted_cloud.go). Builds of this
// repository use it unless built with -ldflags "-X main.hostedCloud=off"
// (build-sidecar-binaries.sh --dev passes that when SOLU_CLOUD_URL is set and
// empty), which leaves the daemon with no hosted cloud at all.
//
// defaultCloudHTTPBase stays the provider host: machines paired before the
// custom domain existed hold it in claim.json, userFacingCloudURL maps it to
// publicAppURL for display, and the desktop shell's Cloud mode, relay and
// sign-in redirect use it too.
var hostedCloud = "on"

func init() { applyHostedCloudDefaults() }

func applyHostedCloudDefaults() {
	if hostedCloud == "off" {
		return
	}
	if defaultCloudHTTPBase == "" {
		defaultCloudHTTPBase = "https://soluterminal-cloud.onrender.com"
	}
	if publicAppURL == "" {
		publicAppURL = "https://app.soluterminal.com"
	}
}
