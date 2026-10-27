package main

import "testing"

func TestHostedCloudOffLeavesNoCloud(t *testing.T) {
	savedMode, savedBase, savedApp := hostedCloud, defaultCloudHTTPBase, publicAppURL
	t.Cleanup(func() { hostedCloud, defaultCloudHTTPBase, publicAppURL = savedMode, savedBase, savedApp })

	hostedCloud, defaultCloudHTTPBase, publicAppURL = "off", "", ""
	applyHostedCloudDefaults()
	if defaultCloudHTTPBase != "" || publicAppURL != "" {
		t.Fatalf("hostedCloud=off set %q / %q", defaultCloudHTTPBase, publicAppURL)
	}

	hostedCloud = "on"
	applyHostedCloudDefaults()
	if defaultCloudHTTPBase == "" || publicAppURL == "" {
		t.Fatal("hostedCloud=on left the hosted cloud unset")
	}
}
