package daemonfeatures

import (
	"reflect"
	"testing"
)

func TestEncodeParseRoundTrip(t *testing.T) {
	raw := Encode([]string{TeamBrokerCodex, " Team-Broker.Claude.V1 ", TeamBrokerCodex, "bad name!", ""})
	if raw != "team-broker.claude.v1,team-broker.codex.v1" {
		t.Fatalf("Encode = %q", raw)
	}
	got := List(Parse(raw))
	if !reflect.DeepEqual(got, []string{TeamBrokerClaude, TeamBrokerCodex}) {
		t.Fatalf("Parse/List = %v", got)
	}
	if len(Parse("")) != 0 {
		t.Fatal("empty handshake must be the empty set (older daemon)")
	}
}

func TestParseCapsCount(t *testing.T) {
	var many []string
	for i := 0; i < MaxFeatures+10; i++ {
		many = append(many, "f"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	if n := len(Parse(Encode(many))); n != MaxFeatures {
		t.Fatalf("kept %d features, want cap %d", n, MaxFeatures)
	}
}
