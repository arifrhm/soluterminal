package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"soluterminal-shared/typesafe"
)

type jevAssertWriteConn struct {
	net.Conn
	assert func()
}

func (c *jevAssertWriteConn) Write(b []byte) (int, error) { c.assert(); return c.Conn.Write(b) }

func TestJevFullEnterTransportRetainsObservationLocks(t *testing.T) {
	a, p, frames := newJevFullHarness(t, jevNovelToolMenu)
	conn := p.agentConn
	p.agentConn = &jevAssertWriteConn{Conn: conn, assert: func() {
		if p.subsMu.TryLock() {
			p.subsMu.Unlock()
			t.Error("screen output not serialized with Enter")
		}
		if p.mu.TryLock() {
			p.mu.Unlock()
			t.Error("policy/connection not serialized with Enter")
		}
		if p.agentWriteMu.TryLock() {
			p.agentWriteMu.Unlock()
			t.Error("input not serialized with Enter")
		}
		if a.mgr.mu.TryLock() {
			a.mgr.mu.Unlock()
			t.Error("membership lease not retained through Enter")
		}
	}}
	a.jevClientFactory = func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
			return jevDetectYesResponse(false), nil
		}}
	}
	a.tryJevAmbiguousTerminal(context.Background(), p, p.ID, p.Title, time.Now())
	expectFrame(t, frames, "one guarded Enter", "\r")
}

func TestJevFullCapturedIdentityNeverDowngradesToUnsupported(t *testing.T) {
	a, p, frames := newJevFullHarness(t, jevNovelToolMenu)
	called := false
	probe := a.jevProcessProbe
	a.jevProcessProbe = func(ctx context.Context, pid int) (jevProcessIdentity, error) {
		if called {
			return jevProcessIdentity{}, errJevProcessUnsupported
		}
		return probe(ctx, pid)
	}
	a.jevClientFactory = func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
			called = true
			return jevDetectYesResponse(false), nil
		}}
	}
	if !a.tryJevAmbiguousTerminal(context.Background(), p, p.ID, p.Title, time.Now()) {
		t.Fatal("captured unsupported observation fell through")
	}
	expectNoFrame(t, frames, "unsupported after provider call")
}

func TestJevFullUnrecognizedFallbackStopsThisObservation(t *testing.T) {
	a, p, frames := newJevFullHarness(t, jevNovelToolMenu)
	a.jevClientFactory = func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
			return nil, context.DeadlineExceeded
		}}
	}
	if !a.tryJevAmbiguousTerminal(context.Background(), p, p.ID, p.Title, time.Now()) {
		t.Fatal("bound observation escaped into a proof-less fallback")
	}
	expectNoFrame(t, frames, "unrecognized timeout")
}

func TestJevOnceLabelAllowsExactEditNotPersistentVariants(t *testing.T) {
	if !ariaJevOnceLabel("Yes, apply it") {
		t.Fatal("one edit permission was rejected")
	}
	for _, label := range []string{"Yes, apply it always", "Yes, apply it (always)", "Yes, apply it permanently", "Yes, apply it and don't ask again", "Yes, apply it indefinitely"} {
		if ariaJevOnceLabel(label) {
			t.Fatalf("persistent or unknown scope accepted: %q", label)
		}
	}
}

func TestJevCapturedSuppressionCannotFallThroughUnbound(t *testing.T) {
	for _, mode := range []string{"paused", "typing", "cautious risk"} {
		t.Run(mode, func(t *testing.T) {
			a, p, frames := newJevFullHarness(t, jevNovelToolMenu)
			calls := 0
			if mode == "paused" {
				a.mu.Lock()
				a.paused = true
				a.mu.Unlock()
			}
			if mode == "typing" {
				atomic.StoreInt64(&p.LastInputMs, time.Now().UnixMilli())
			}
			if mode == "cautious risk" {
				p.mu.Lock()
				p.AutoMode = "cautious"
				p.mu.Unlock()
				if err := os.WriteFile(filepath.Join(a.dataDir, ariaJevDetectMarker), []byte(p.ID+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			a.jevClientFactory = func(string) jevSystemOneClient {
				return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
					calls++
					r := jevDetectYesResponse(true)
					low := .1
					r.Answers["safe_action"] = typesafe.Answer{Type: "noul", Noul: &low}
					return r, nil
				}}
			}
			handled := a.tryJevAmbiguousTerminal(context.Background(), p, p.ID, p.Title, time.Now())
			// Clearing the suppression and replacing the session must begin a
			// fresh observation, not continue the old caller's proof-less path.
			a.mu.Lock()
			a.paused = false
			a.mu.Unlock()
			atomic.StoreInt64(&p.LastInputMs, 0)
			p.SetCodexThreadID("replacement-after-suppression")
			if !handled {
				t.Error("captured observation returned to unbound incumbent")
			}
			expectNoFrame(t, frames, "suppressed/risk rejected")
			want := 0
			if mode == "cautious risk" {
				want = 1
			}
			if calls != want {
				t.Fatalf("provider calls=%d want=%d", calls, want)
			}
		})
	}
}

// Fixtures provide complete, synthetic process identity through a per-Aria
// dependency. Production never exempts panes with missing PIDs from probing.
func seedJevTestProcess(a *Aria, p *Pane) {
	p.Pid = 77
	p.CreatedAt = time.Unix(1000, 0).UTC()
	p.Agent = "codex"
	p.CodexThreadID = "synthetic-codex-session"
	a.jevProcessProbe = func(ctx context.Context, pid int) (jevProcessIdentity, error) {
		return jevProcessIdentity{Source: "synthetic-test", RootPID: pid, RootStart: "1000"}, ctx.Err()
	}
}

func TestJevFullAllObservedIdentityChangesStopPendingAnswer(t *testing.T) {
	for _, field := range []string{"pid", "created", "cwd", "kind", "provider", "agent", "harness", "muse", "connection", "membership", "process", "process unreadable"} {
		t.Run(field, func(t *testing.T) {
			a, p, frames := newJevFullHarness(t, jevNovelToolMenu)
			processChanged := false
			originalProbe := a.jevProcessProbe
			a.jevProcessProbe = func(ctx context.Context, pid int) (jevProcessIdentity, error) {
				identity, err := originalProbe(ctx, pid)
				if processChanged {
					if field == "process unreadable" {
						return jevProcessIdentity{}, context.DeadlineExceeded
					}
					identity.Digest[0] = 1
				}
				return identity, err
			}
			a.jevClientFactory = func(string) jevSystemOneClient {
				return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
					if field == "membership" {
						replacement := testPaneWithTail(t, p.ID, p.Title, jevNovelToolMenu)
						replacement.AutoMode = "always"
						a.mgr.mu.Lock()
						a.mgr.panes[p.ID] = replacement
						a.mgr.mu.Unlock()
						return jevDetectYesResponse(false), nil
					}
					if strings.HasPrefix(field, "process") {
						processChanged = true
						return jevDetectYesResponse(false), nil
					}
					p.mu.Lock()
					switch field {
					case "pid":
						p.Pid++
					case "created":
						p.CreatedAt = p.CreatedAt.Add(time.Second)
					case "cwd":
						p.Cwd = "/synthetic-changed-cwd"
					case "kind":
						p.Kind = "native"
					case "provider":
						p.Provider = "changed-provider"
					case "agent":
						p.Agent = "claude"
					case "harness":
						p.HarnessSessionID = "changed-harness"
					case "muse":
						p.MuseSessionID = "changed-muse"
					case "connection":
						local, remote := net.Pipe()
						p.agentConn = local
						t.Cleanup(func() { local.Close(); remote.Close() })
					}
					p.mu.Unlock()
					return jevDetectYesResponse(false), nil
				}}
			}
			a.evaluateObservation(context.Background(), p.ID, p.Title, "")
			expectNoFrame(t, frames, "changed "+field)
		})
	}
}

func TestJevFullPersistentParentheticalOrRollbackNeverFires(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		for _, label := range []string{"Yes, proceed (always)", "Yes, proceed (permanent)", "Yes, allow this tool permanently"} {
			t.Run(label+map[bool]string{false: "/full", true: "/rollback"}[disabled], func(t *testing.T) {
				a, p, frames := newJevFullHarness(t, strings.Replace(codexCommandApprovalScreen, "Yes, proceed (y)", label, 1))
				if disabled {
					t.Setenv(ariaJevFullEnv, "0")
				}
				a.jevClientFactory = func(string) jevSystemOneClient { t.Fatal("persistent grant reached Jev"); return nil }
				a.evaluateObservation(context.Background(), p.ID, p.Title, "")
				expectNoFrame(t, frames, "persistent/rollback")
			})
		}
	}
}

func TestJevFullWrappedQuestionFootersNeverReachProvider(t *testing.T) {
	for _, footer := range []string{"Enter to\nselect", "Enter\nto select"} {
		a, p, frames := newJevFullHarness(t, jevNovelToolMenu+"\n"+footer)
		a.jevClientFactory = func(string) jevSystemOneClient { t.Fatal("question footer reached Jev"); return nil }
		a.evaluateObservation(context.Background(), p.ID, p.Title, "")
		expectNoFrame(t, frames, "wrapped question")
	}
}

func bindJevTestProof(t *testing.T, a *Aria, p *Pane, proof *jevAmbiguousProof) {
	t.Helper()
	proof.identity, proof.connection = captureJevPaneIdentity(p)
	proof.gridGeneration = atomic.LoadUint64(&p.GridChoicePromptGeneration)
	proof.probe = a.jevProcessProbe
	var err error
	proof.process, err = proof.probe(context.Background(), p.Pid)
	if err != nil {
		t.Fatal(err)
	}
}

func TestJevFullFinalAutoModeChangeNeverWrites(t *testing.T) {
	a, p, frames := newJevFullHarness(t, jevNovelToolMenu)
	tail := ariaJevPermissionTail(p, true)
	_, pos, fp, ok := currentChoicePromptIdentity(p, tail)
	if !ok {
		t.Fatal("missing identity")
	}
	proof := &jevAmbiguousProof{dataDir: a.dataDir, mode: "always", full: true, classified: true, pos: pos, fingerprint: fp, tailDigest: sha256.Sum256([]byte(ariaJevMenuEvidence(tail, true)))}
	bindJevTestProof(t, a, p, proof)
	probe := proof.probe
	proof.probe = func(ctx context.Context, pid int) (jevProcessIdentity, error) {
		p.mu.Lock()
		p.AutoMode = "off"
		p.mu.Unlock()
		return probe(ctx, pid)
	}
	a.writeJevEnter(context.Background(), p, proof)
	expectNoFrame(t, frames, "AutoMode disabled during final probe")
}

func TestJevFullProjectionClosesBeforeNumberedPrivateOutput(t *testing.T) {
	tail := jevNovelToolMenu + "\n\nunrelated private conversation\n  3. unrelated numbered output"
	a, p, _ := newJevFullHarness(t, tail)
	calls := 0
	a.jevClientFactory = func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(_ context.Context, _ string, data any, _ map[string]typesafe.Question) (*typesafe.Response, error) {
			calls++
			if data != jevNovelToolMenu {
				t.Errorf("private continuation exported: %q", data)
			}
			return jevDetectYesResponse(false), nil
		}}
	}
	a.tryJevAmbiguousTerminal(context.Background(), p, p.ID, p.Title, time.Now())
	if calls != 1 {
		t.Fatalf("safe projected menu calls=%d", calls)
	}
}

func TestJevFullDecodedUsageIsUnknownNotZero(t *testing.T) {
	for _, usage := range []string{"", `,"usage":null`, `,"usage":{}`, `,"usage":{"input_tokens":null}`, `,"usage":{"input_tokens":0}`} {
		t.Run(usage, func(t *testing.T) {
			a, p, frames := newJevFullHarness(t, jevNovelToolMenu)
			var buf bytes.Buffer
			old := log.Writer()
			log.SetOutput(&buf)
			defer log.SetOutput(old)
			a.jevClientFactory = func(string) jevSystemOneClient {
				return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
					var response typesafe.Response
					err := json.Unmarshal([]byte(`{"model":"`+ariaJevModel+`","answers":{"permission_menu":{"type":"noul","noul":0.99}}`+usage+`}`), &response)
					return &response, err
				}}
			}
			a.tryJevAmbiguousTerminal(context.Background(), p, p.ID, p.Title, time.Now())
			want := "input_tokens=-1"
			cost := "estimated_cost_usd=unknown"
			if strings.Contains(usage, ":0}") {
				want = "input_tokens=0"
				cost = "estimated_cost_usd=0.00000000"
				expectFrame(t, frames, "Jev-approved permission with metering observation", "\r")
			} else {
				// Unmetered answers are rejected; an unrecognized menu has no
				// local fallback.
				expectNoFrame(t, frames, "unmetered Jev answer")
			}
			if !strings.Contains(buf.String(), want) || !strings.Contains(buf.String(), cost) {
				t.Fatalf("wrong decoded usage: %s", buf.String())
			}
		})
	}
}

func TestJevFullExportsOnlyActivePermission(t *testing.T) {
	for _, outside := range []string{"unrelated private conversation", `{"password":"synthetic-test-password"}`} {
		for _, suffix := range []bool{false, true} {
			t.Run(outside+"/"+map[bool]string{false: "above", true: "below"}[suffix], func(t *testing.T) {
				tail := outside + "\n" + jevNovelToolMenu
				if suffix {
					tail = jevNovelToolMenu + "\n\n" + outside
				}
				a, p, _ := newJevFullHarness(t, tail)
				calls := 0
				a.jevClientFactory = func(string) jevSystemOneClient {
					return ariaJevSafetyClient{call: func(_ context.Context, _ string, data any, _ map[string]typesafe.Question) (*typesafe.Response, error) {
						calls++
						if data != jevNovelToolMenu {
							t.Errorf("exported unrelated terminal content: %q", data)
						}
						return jevDetectYesResponse(false), nil
					}}
				}
				a.tryJevAmbiguousTerminal(context.Background(), p, p.ID, p.Title, time.Now())
				if calls != 1 {
					t.Fatalf("safe projected menu should classify once, calls=%d", calls)
				}
			})
		}
	}
}

func TestJevFullDoesNotExportQuotedActionSecret(t *testing.T) {
	for _, key := range []string{"password", "passwd", "secret", "token", "api-key"} {
		t.Run(key, func(t *testing.T) {
			tail := strings.Replace(jevNovelToolMenu, "src/app.go", `{"`+key+`":"synthetic-value"}`, 1)
			a, p, frames := newJevFullHarness(t, tail)
			a.jevClientFactory = func(string) jevSystemOneClient { t.Error("action secret reached provider"); return nil }
			a.evaluateObservation(context.Background(), p.ID, p.Title, "")
			expectNoFrame(t, frames, "secret action")
		})
	}
}

func TestJevFullSessionReplacementCannotFireOrReuseCache(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			a, p, frames := newJevFullHarness(t, jevNovelToolMenu)
			setSession := p.SetCodexThreadID
			if provider == "claude" {
				setSession = p.SetClaudeSessionID
			}
			setSession("session-before")
			a.jevClientFactory = func(string) jevSystemOneClient {
				return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
					setSession("session-after")
					return jevDetectYesResponse(false), nil
				}}
			}
			a.evaluateObservation(context.Background(), p.ID, p.Title, "")
			expectNoFrame(t, frames, "replacement session")
		})
	}
	t.Run("negative cache", func(t *testing.T) {
		a, p, _ := newJevFullHarness(t, jevNovelToolMenu)
		p.SetCodexThreadID("session-before")
		calls := 0
		a.jevClientFactory = func(string) jevSystemOneClient {
			return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
				calls++
				r := jevDetectYesResponse(false)
				low := .1
				r.Answers["permission_menu"] = typesafe.Answer{Type: "noul", Noul: &low}
				return r, nil
			}}
		}
		a.tryJevAmbiguousTerminal(context.Background(), p, p.ID, p.Title, time.Now())
		p.SetCodexThreadID("session-after")
		a.tryJevAmbiguousTerminal(context.Background(), p, p.ID, p.Title, time.Now())
		if calls != 2 {
			t.Fatalf("cache crossed session boundary, calls=%d", calls)
		}
	})
}

func TestJevFullPersistentOptionCannotReachDeterministicFallback(t *testing.T) {
	for _, label := range []string{"Yes, allow this tool permanently", "Yes, allow this tool indefinitely", "Yes, permit all subsequent requests"} {
		t.Run(label, func(t *testing.T) {
			tail := strings.Replace(codexCommandApprovalScreen, "Yes, proceed", label, 1)
			if tail == codexCommandApprovalScreen {
				t.Fatal("fixture label changed")
			}
			a, p, frames := newJevFullHarness(t, tail)
			a.jevClientFactory = func(string) jevSystemOneClient { t.Error("persistent option reached provider"); return nil }
			a.evaluateObservation(context.Background(), p.ID, p.Title, "")
			expectNoFrame(t, frames, "persistent grant")
		})
	}
}

func TestJevFullPreservesPaidUsageForRejectedResponse(t *testing.T) {
	for _, outcome := range []string{"stale", "cancelled", "wrong model", "missing response"} {
		t.Run(outcome, func(t *testing.T) {
			a, p, frames := newJevFullHarness(t, jevNovelToolMenu)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var buf bytes.Buffer
			old := log.Writer()
			log.SetOutput(&buf)
			defer log.SetOutput(old)
			a.jevClientFactory = func(string) jevSystemOneClient {
				return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
					r := jevDetectYesResponse(false)
					r.Usage.InputTokens = 137
					switch outcome {
					case "stale":
						changed := strings.Replace(jevNovelToolMenu, "src/app.go", "src/other.go", 1)
						appendDirectAutoMenuRedraw(t, p, changed)
						seedGridMenu(t, p, changed)
					case "cancelled":
						cancel()
					case "wrong model":
						r.Model = "unknown-model"
					case "missing response":
						return nil, context.DeadlineExceeded
					}
					return r, nil
				}}
			}
			a.tryJevAmbiguousTerminal(ctx, p, p.ID, p.Title, time.Now())
			// An unrecognized menu has no local fallback: a rejected provider
			// answer leaves it for the human.
			expectNoFrame(t, frames, "rejected response")
			want := "input_tokens=137"
			if outcome == "missing response" {
				want = "input_tokens=-1"
			}
			if !strings.Contains(buf.String(), want) {
				t.Fatalf("lost observed usage, want %s in %s", want, buf.String())
			}
			if (outcome == "wrong model" || outcome == "missing response") && !strings.Contains(buf.String(), "estimated_cost_usd=unknown") {
				t.Fatalf("unknown cost presented as priced: %s", buf.String())
			}
		})
	}
}
