package main

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"soluterminal-shared/daemonfeatures"
)

func TestHealthAdvertisesDirectOwnerCapability(t *testing.T) {
	a := &apiServer{mgr: NewManager(""), startT: time.Now(), ver: "dev"}
	response := httptest.NewRecorder()
	a.health(response, httptest.NewRequest("GET", "/api/health", nil))
	var health struct {
		Features []string `json:"features"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(health.Features, daemonfeatures.WorkflowOwnerDirect) {
		t.Fatalf("direct owner capability missing from health: %v", health.Features)
	}
	if !daemonfeatures.Parse(daemonfeatures.Encode(daemonFeatures))[daemonfeatures.WorkflowOwnerDirect] {
		t.Fatal("direct owner capability missing from relay handshake")
	}
}
