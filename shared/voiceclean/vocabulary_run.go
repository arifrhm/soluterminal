package voiceclean

import "context"

// RunWithVocabulary runs a transcription with the vocabulary prompt and checks
// short answers against an unprompted run (see ChooseTranscript). run does one
// request ("" means no prompt); textOf reports its text and whether it
// succeeded. For small recordings the unprompted run starts alongside the
// prompted one, so the check adds no wait. The returned note says why the
// unprompted answer was used ("" when the prompted one was kept).
func RunWithVocabulary[R any](ctx context.Context, audioBytes int, language string, custom []string, prompt string,
	run func(ctx context.Context, prompt string) R, textOf func(R) (string, bool)) (R, string) {
	return RunWithVocabularyMode(ctx, audioBytes, language, custom, prompt, true, run, textOf)
}

// RunWithVocabularyMode is RunWithVocabulary with the small-clip dual run
// switchable. With smallClipDual false, a small recording sends only the
// prompted request; the unprompted run is made afterwards, one at a time,
// and only when the prompted answer looks hallucinated (foreign script or an
// echo of the prompt, PromptedResultSuspect). The "six words or fewer" check
// is skipped for small clips, because nearly every short command is that
// short and the check would cost the same second request, just later.
//
// The trade-off, from the 2026-10-22 replay (160 synthetic clips and 20
// silence probes; launch/reviews/soluterminal-voice/data/vocab/simulate.txt):
// on real speech the unprompted check never changed the final answer (WER
// and names identical with or without it). Its whole value is on room tone
// and near-silence, where a prompted Whisper invents name lists ("Scefie,
// Kudel, GitHub, Tauri.", "Rol, Rol, Rol…"); the check turned 2 of 10
// language=en probes back into no speech. Without it those can reach the
// pane as stray words unless the caller's silence-hallucination guard
// (SilenceHallucination) catches them. A prompted failure (e.g. a rate
// limit) is also no longer rescued by an unprompted run already in flight.
// In exchange, short clips use one upstream request instead of two.
func RunWithVocabularyMode[R any](ctx context.Context, audioBytes int, language string, custom []string, prompt string,
	smallClipDual bool, run func(ctx context.Context, prompt string) R, textOf func(R) (string, bool)) (R, string) {
	if prompt == "" {
		return run(ctx, ""), ""
	}
	small := audioBytes <= SmallAudioBytes
	var early chan R
	if small && smallClipDual {
		early = make(chan R, 1)
		go func() { early <- run(ctx, "") }()
	}
	unprompted := func() R {
		if early != nil {
			return <-early
		}
		return run(ctx, "")
	}

	p := run(ctx, prompt)
	ptext, pok := textOf(p)
	if !pok {
		// Only fall back when the unprompted run is already under way: a
		// failure such as a rate limit would hit a fresh request too.
		if early != nil {
			if u := unprompted(); func() bool { _, ok := textOf(u); return ok }() {
				return u, "prompt_failed"
			}
		}
		return p, ""
	}
	needsCheck := NeedsUnpromptedCheck(ptext, language, custom)
	if small && !smallClipDual && needsCheck {
		needsCheck, _ = PromptedResultSuspect(ptext, language, custom)
	}
	if !needsCheck {
		return p, ""
	}
	u := unprompted()
	utext, uok := textOf(u)
	if !uok {
		return p, ""
	}
	if _, used, reason := ChooseTranscript(ptext, utext, language, custom); used {
		return u, reason
	}
	return p, ""
}
