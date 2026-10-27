package main

import (
	"os"
	"testing"
)

// Every fixture below is shaped from records observed in REAL transcripts under
// ~/.claude/projects (claude 2.1.211), not invented: a killed agent writes no
// assistant record at all; a user ESC writes "[Request interrupted by user]" as
// a list-content text block; an ESC during a tool writes "…for tool use" AFTER
// the tool_result; /compact leaves a `<local-command-stdout>Compacted…` stub
// with NO promptSource; task notifications arrive with promptSource=system.
func TestClaudeTranscriptAwaitsReply(t *testing.T) {
	const asst = `{"type":"assistant","uuid":"a1","message":{"id":"m1","content":[{"type":"text","text":"here you go"}]}}`
	const prompt = `{"type":"user","promptSource":"typed","uuid":"u1","message":{"role":"user","content":"do the thing"}}`

	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			// THE case this exists for: killed while streaming text/thinking. No
			// assistant record is ever written, so there is no dangling tool_use
			// and the transcript just stops after the prompt. The attachment
			// records trailing the prompt are exactly what the real probe left.
			name: "died mid-text: prompt then nothing",
			body: prompt + "\n" +
				`{"type":"attachment","attachment":{"type":"skill_listing","content":"..."}}` + "\n",
			want: true,
		},
		{
			name: "healthy: assistant answered",
			body: prompt + "\n" + asst + "\n",
			want: false,
		},
		{
			// A DELIBERATE stop always writes the marker. This is the false
			// positive the whole design turns on: without it, every ESC before
			// the first token would claim a restart ate the response.
			name: "user ESC before any assistant text",
			body: prompt + "\n" +
				`{"type":"user","uuid":"u2","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]}}` + "\n",
			want: false,
		},
		{
			name: "user ESC after partial assistant text",
			body: prompt + "\n" + asst + "\n" +
				`{"type":"user","uuid":"u2","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]}}` + "\n",
			want: false,
		},
		{
			// Observed shape: the "for tool use" marker follows the tool_result.
			name: "user ESC during a tool call",
			body: prompt + "\n" +
				`{"type":"assistant","uuid":"a1","message":{"id":"m1","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]}}` + "\n" +
				`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"t1","type":"tool_result","content":"ok"}]}}` + "\n" +
				`{"type":"user","uuid":"u2","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}` + "\n",
			want: false,
		},
		{
			// The API requires an assistant reply after a tool_result, so a
			// transcript stopping here died mid-turn — and no marker means it
			// wasn't the user's doing.
			name: "died after a tool result",
			body: prompt + "\n" +
				`{"type":"assistant","uuid":"a1","message":{"id":"m1","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]}}` + "\n" +
				`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"t1","type":"tool_result","content":"ok"}]}}` + "\n",
			want: true,
		},
		{
			// THE trap. /compact writes this stub and the session sits idle,
			// perfectly healthy. Reading it as a pending turn would tell a user
			// who just ran /compact that a restart ate their response.
			name: "compaction stub is not a pending turn",
			body: prompt + "\n" + asst + "\n" +
				`{"type":"user","message":{"role":"user","content":"<local-command-stdout>Compacted (ctrl+o to see full summary)</local-command-stdout>"}}` + "\n",
			want: false,
		},
		{
			// promptSource=system: a background wake-up, not a turn a human is
			// sitting there waiting on. Matches mapTranscriptUser's allow-list.
			name: "task notification is not a human turn",
			body: prompt + "\n" + asst + "\n" +
				`{"type":"user","promptSource":"system","message":{"role":"user","content":"<task-notification>done</task-notification>"}}` + "\n",
			want: false,
		},
		{
			name: "meta-flagged user line ignored",
			body: prompt + "\n" + asst + "\n" +
				`{"type":"user","promptSource":"typed","isMeta":true,"message":{"role":"user","content":"caveat text"}}` + "\n",
			want: false,
		},
		{
			name: "compact summary flag ignored",
			body: prompt + "\n" + asst + "\n" +
				`{"type":"user","promptSource":"typed","isCompactSummary":true,"message":{"role":"user","content":"This session is being continued..."}}` + "\n",
			want: false,
		},
		{
			// A summary that merely QUOTES the marker mid-text must not read as
			// an interrupt — hence prefix matching on the block, never a
			// substring search over the record.
			name: "text quoting the marker is not an interrupt",
			body: prompt + "\n" +
				`{"type":"user","promptSource":"typed","message":{"role":"user","content":"earlier you saw [Request interrupted by user] and stopped"}}` + "\n",
			want: true,
		},
		{
			// A subagent's internals must not settle the MAIN turn.
			name: "sidechain assistant does not answer the main turn",
			body: prompt + "\n" +
				`{"type":"assistant","uuid":"a9","isSidechain":true,"message":{"id":"m9","content":[{"type":"text","text":"subagent chatter"}]}}` + "\n",
			want: true,
		},
		{
			name: "queued prompt counts as a real turn",
			body: asst + "\n" +
				`{"type":"user","promptSource":"queued","message":{"role":"user","content":"and also this"}}` + "\n",
			want: true,
		},
		{
			// The VERBATIM shape a real killed agent left on disk: a human turn
			// submitted as content BLOCKS records promptSource=sdk with list
			// content, not the string form a typed turn uses. Reading only the
			// string form would miss the exact case this feature exists for.
			name: "block-shaped sdk prompt is a real turn",
			body: `{"type":"user","promptSource":"sdk","uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"Count slowly from 1 to 40"}]}}` + "\n" +
				`{"type":"attachment","attachment":{"type":"skill_listing","content":"..."}}` + "\n",
			want: true,
		},
		{
			// Same block shape, but machinery rather than a human turn.
			name: "block-shaped record without a real promptSource ignored",
			body: prompt + "\n" + asst + "\n" +
				`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"some injected block"}]}}` + "\n",
			want: false,
		},
		{
			name: "empty transcript",
			body: "",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cwd := "/home/user/wsl-projects/awaits-" + t.Name()
			path := writeSessionTranscript(t, t.TempDir(), cwd, "sess-awaits", tt.body)
			tail, err := scanClaudeTranscriptTail(path)
			if err != nil {
				t.Fatalf("scan err = %v", err)
			}
			if tail.awaitsReply != tt.want {
				t.Fatalf("awaitsReply = %v, want %v", tail.awaitsReply, tt.want)
			}
		})
	}
}

// prepareClaudeResume must report the mid-text death as a notice-worthy state
// WITHOUT touching the transcript: there is nothing to repair (no dangling
// tool_use), the session resumes fine — the user just needs to be told their
// answer evaporated.
func TestPrepareClaudeResumeReportsMidTextDeathWithoutRepair(t *testing.T) {
	cwd, sess := "/home/user/wsl-projects/midtext-proj", "midtext-session"
	body := `{"type":"user","promptSource":"typed","uuid":"u1","message":{"role":"user","content":"write me a long essay"}}` + "\n"
	path := writeSessionTranscript(t, t.TempDir(), cwd, sess, body)

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := prepareClaudeResume(cwd, sess)
	if err != nil {
		t.Fatalf("prepare err = %v", err)
	}
	if st.repairedToolUse {
		t.Fatal("a mid-text death has no dangling tool_use — nothing should be repaired")
	}
	if !st.awaitsReply {
		t.Fatal("a transcript stopping at the user's prompt must report awaitsReply (→ the notice)")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("prepareClaudeResume must not modify a transcript it has nothing to repair")
	}
}

// A healthy session (agent answered, user walked away) must stay silent — this
// is 109 of the 120 most recent real transcripts, so a false positive here
// would put a wrong banner on nearly every resume.
func TestPrepareClaudeResumeSilentOnHealthySession(t *testing.T) {
	cwd, sess := "/home/user/wsl-projects/healthy-proj", "healthy-session"
	body := `{"type":"user","promptSource":"typed","uuid":"u1","message":{"role":"user","content":"hi"}}` + "\n" +
		`{"type":"assistant","uuid":"a1","message":{"id":"m1","content":[{"type":"text","text":"hello"}]}}` + "\n"
	writeSessionTranscript(t, t.TempDir(), cwd, sess, body)

	st, err := prepareClaudeResume(cwd, sess)
	if err != nil {
		t.Fatalf("prepare err = %v", err)
	}
	if st.repairedToolUse || st.awaitsReply {
		t.Fatalf("healthy session must produce no notice, got %+v", st)
	}
}
