package main

import "testing"

// The pairing box shows people the branded address for the default cloud,
// and any other relay as configured.
func TestUserFacingCloudURL(t *testing.T) {
	oldBase, oldApp := defaultCloudHTTPBase, publicAppURL
	defaultCloudHTTPBase, publicAppURL = "https://hosted-cloud.example", "https://app.hosted.example"
	t.Cleanup(func() { defaultCloudHTTPBase, publicAppURL = oldBase, oldApp })
	cases := map[string]string{
		defaultCloudHTTPBase:        "https://app.hosted.example",
		defaultCloudHTTPBase + "/":  "https://app.hosted.example",
		"https://relay.example.com": "https://relay.example.com",
		"http://127.0.0.1:9000":     "http://127.0.0.1:9000",
	}
	for in, want := range cases {
		if got := userFacingCloudURL(in); got != want {
			t.Fatalf("userFacingCloudURL(%q) = %q, want %q", in, got, want)
		}
	}
	base, err := relayToHTTPBase("wss://hosted-cloud.example/relay")
	if err != nil {
		t.Fatal(err)
	}
	if got := userFacingCloudURL(base); got != "https://app.hosted.example" {
		t.Fatalf("default relay shows %q", got)
	}
}

// With no hosted cloud built in, every address is shown as configured.
func TestUserFacingCloudURLWithoutHostedCloud(t *testing.T) {
	oldBase, oldApp := defaultCloudHTTPBase, publicAppURL
	defaultCloudHTTPBase, publicAppURL = "", ""
	t.Cleanup(func() { defaultCloudHTTPBase, publicAppURL = oldBase, oldApp })
	for _, in := range []string{"", "https://relay.example.com"} {
		if got := userFacingCloudURL(in); got != in {
			t.Fatalf("userFacingCloudURL(%q) = %q, want it unchanged", in, got)
		}
	}
}
