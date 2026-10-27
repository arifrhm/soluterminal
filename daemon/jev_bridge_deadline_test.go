package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"soluterminal-shared/typesafe"
	"soluterminal/jev"
)

// Models the interval between the absolute deadline passing and the context's
// asynchronous timer callback closing Done. No sleeps or timer scheduling are
// involved, so the regression is deterministic even under suite CPU pressure.
type jevBridgePendingDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c jevBridgePendingDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }

func TestJevBridgeRejectsLateSuccessWithoutTimerNotification(t *testing.T) {
	t.Setenv(jevBridgeModelEnv, "")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want int
	}{
		{"deadline elapsed but Err still nil", jevBridgePendingDeadlineContext{context.Background(), time.Now().Add(-time.Hour)}, http.StatusGatewayTimeout},
		{"valid response before deadline", jevBridgePendingDeadlineContext{context.Background(), time.Now().Add(time.Hour)}, http.StatusOK},
		{"client ignores request cancellation", canceled, http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			b, logs := enabledBridgeForTest(t, bridgeTestClient{call: func(ctx context.Context, model string, _ any, questions map[string]typesafe.Question) (*typesafe.Response, error) {
				calls++
				if tc.want == http.StatusGatewayTimeout && ctx.Err() != nil {
					t.Fatal("fixture did not reproduce the missing timer notification")
				}
				return validBridgeResponse(model, questions), nil
			}})
			body, err := json.Marshal(validBridgeInput(jev.DecisionActionOutcome))
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787"+jevBridgePath, bytes.NewReader(body)).WithContext(tc.ctx)
			r.RemoteAddr = "127.0.0.1:54321"
			r.Header.Set("Authorization", "Bearer "+bridgeTestToken)
			w := httptest.NewRecorder()
			b.ServeHTTP(w, r)
			if w.Code != tc.want || calls != 1 {
				t.Fatalf("status=%d want=%d provider_calls=%d", w.Code, tc.want, calls)
			}
			if tc.want == http.StatusGatewayTimeout {
				if !strings.Contains(w.Body.String(), "provider_timeout") || !strings.Contains(logs.String(), "outcome=timeout") || strings.Contains(w.Body.String(), `"answers"`) {
					t.Fatal("late provider success was not rejected and audited")
				}
			}
			if tc.want == http.StatusBadGateway && (!strings.Contains(w.Body.String(), "provider_error") || strings.Contains(w.Body.String(), `"answers"`)) {
				t.Fatal("canceled provider success was not rejected")
			}
			for _, secret := range []string{bridgeAPIKey, bridgeTestToken, "intent-sentinel", "state-sentinel"} {
				if strings.Contains(w.Body.String()+logs.String(), secret) {
					t.Fatal("response or audit leaked request/authentication data")
				}
			}
		})
	}
}
