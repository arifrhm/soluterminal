import { fireEvent, render, screen, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import type { ReleaseTrainPreview, ReleaseTrainStatus } from '../api';
import type { ReleaseCheck, ReleaseReadiness } from '../types';
import { ReleasePopover } from './ReleasePopover';

const readiness: ReleaseReadiness = {
  state: 'ok', lastReleaseVersion: '0.0.990', lastReleaseAnchor: 'abc123',
  anchorSource: 'published-manifest', anchorProvenance: 'exact', unreleasedTotal: 4,
  functionalCount: 3, docsOnlyCount: 1, functionalPaths: ['daemon', 'web'],
  suggestedNextVersion: '0.0.991', oldestUnreleasedAt: '2026-08-09T00:00:00Z',
  runningVersion: '0.0.990', dirty: false, computedAt: '2026-08-10T00:00:00Z',
  functionalCommitCount: 3, oldestFunctionalUnreleasedAt: '2026-08-15T09:12:00Z',
};

function anchor(): HTMLButtonElement {
  const button = document.createElement('button');
  document.body.appendChild(button);
  return button;
}

function check(overrides: Partial<ReleaseCheck> = {}): ReleaseCheck {
  return {
    allowed: false,
    cut: {
      id: 'rc_12345678', version: '0.0.991', repoKey: '/repo/.git',
      sourceSha: '0123456789abcdef0123456789abcdef01234567', declaredBy: 'api', status: 'open',
      createdAt: '2026-08-10T00:00:00Z', updatedAt: '2026-08-10T00:00:00Z',
    },
    blockers: [{ taskId: 'wft_354752a3', title: 'Reconcile Muse review prepass', status: 'awaiting_approval', queueState: 'awaiting_approval', tier: 'critical', why: 'awaits human approval' }],
    waived: [{ taskId: 'wft_aaaaaaaa', reason: 'unrelated documentation experiment', by: 'tasks-ui' }],
    included: [{ taskId: 'wft_bbbbbbbb', title: 'Release prerequisite', status: 'completed', landOutcome: 'merged', ancestor: 'verified' }],
    warnings: [],
    ...overrides,
  };
}

function preview(overrides: Partial<ReleaseTrainPreview> = {}): ReleaseTrainPreview {
  return {
    evaluatedAt: '2026-08-15T12:00:00Z',
    ref: 'origin/main',
    version: '0.0.991',
    functionalLandings: 3,
    minFunctionalLandings: 6,
    oldestFunctionalAt: '2026-08-15T09:12:00Z',
    ageHours: 2.8,
    maxAgeHours: 12,
    landingsRemaining: 3,
    ageTriggerAt: '2026-08-15T21:12:00Z',
    engine: { running: 0, pending: 0, awaitingApproval: 0, quiet: true },
    eligible: false,
    reasons: ['3 of 6 functional landings and oldest 2.8h of 12h'],
    ...overrides,
  };
}

function train(overrides: Partial<ReleaseTrainStatus> = {}): ReleaseTrainStatus {
  return {
    policy: {
      enabled: true, minFunctionalLandings: 6, maxUnreleasedAgeHours: 12,
      quietRequired: true, notifyVetoMinutes: 10,
    },
    state: {
      phase: 'declared',
      train: {
        id: 'rtr_12345678', version: '0.0.991', sourceSha: 'abc1234', status: 'declared',
        declaredAt: '2026-08-15T12:00:00Z', departAt: '2026-08-15T12:10:00Z',
      },
      parked: null,
      restartPending: null,
    },
    preview: null,
    events: [],
    ...overrides,
  };
}

describe('ReleasePopover', () => {
  it('renders a blocked scoped release with blocker state and waiver reason', () => {
    render(<ReleasePopover anchor={anchor()} readiness={readiness} releaseCheck={check()} onClose={() => {}} />);
    expect(screen.getByText('BLOCKED — 1 task(s) in scope')).toBeInTheDocument();
    const dialog = screen.getByRole('dialog', { name: 'Release readiness' });
    expect(within(dialog).getByText('Release scope').closest('details')).toHaveAttribute('open');
    expect(within(dialog).getByText(/Reconcile Muse review prepass/)).toBeInTheDocument();
    expect(within(dialog).getByText('needs approval')).toBeInTheDocument();
    expect(within(dialog).getByText('unrelated documentation experiment')).toBeInTheDocument();
    expect(within(dialog).getByText('0123456789abcdef0123456789abcdef01234567')).toBeInTheDocument();
    expect(within(dialog).getByText('exact provenance')).toBeInTheDocument();
  });

  it('renders typed modern and legacy infrastructure diagnoses with retained reasons', () => {
    render(<ReleasePopover anchor={anchor()} readiness={readiness} releaseCheck={check({
      blockers: [{
        taskId: 'wft_browser', title: 'Browser acceptance', status: 'completed', landOutcome: 'needs_merge',
        why: 'required browser infrastructure unavailable', landBlocker: 'browser_accept_stale',
        infraCode: 'runtime_version_skew', infraReason: 'contract mismatch',
        conflictPaths: ['daemon/a.go'], blockedByTaskIds: ['wft_parent'],
        clearAction: 'Start the current runtime, then Re-land.',
      }, {
        taskId: 'wft_legacy_browser', title: 'Legacy browser acceptance', status: 'completed', landOutcome: 'needs_merge',
        why: 'required browser infrastructure unavailable', landBlocker: 'browser_accept_stale',
        infraCode: 'unknown_legacy', infraReason: 'old daemon endpoint unavailable',
        clearAction: 'Inspect the legacy browser-infrastructure reason, restore it, then Re-land.',
      }],
      deadlocks: [{
        code: 'release_runtime_activation_cycle', blockerTaskId: 'wft_browser', activationTaskIds: ['wft_activation'],
        cause: 'the cut contains the runtime fix', breakAction: 'Start a runtime from this cut, then Re-land.',
      }],
    })} onClose={() => {}} />);
    const dialog = screen.getByRole('dialog', { name: 'Release readiness' });
    expect(within(dialog).getByText('runtime_version_skew')).toBeInTheDocument();
    expect(within(dialog).getByText('unknown_legacy')).toBeInTheDocument();
    expect(within(dialog).getByText(/old daemon endpoint unavailable/)).toBeInTheDocument();
    expect(within(dialog).getByText('daemon/a.go')).toBeInTheDocument();
    expect(within(dialog).getByText('wft_parent')).toBeInTheDocument();
    const alert = within(dialog).getByRole('alert');
    expect(alert).toHaveTextContent(/Release\/merge cycle detected/);
    expect(alert).toHaveTextContent(/wft_activation/);
  });

  it('renders an allowed verdict with the captured source sha', () => {
    render(<ReleasePopover anchor={anchor()} readiness={readiness} releaseCheck={check({ allowed: true, blockers: [] })} onClose={() => {}} />);
    expect(screen.getByText('Scope safe · v0.0.991 · source 0123456')).toBeInTheDocument();
  });

  it('keeps the legacy popover unchanged when release-check is absent', () => {
    render(<ReleasePopover anchor={anchor()} readiness={{ ...readiness, anchorProvenance: 'inferred' }} onClose={() => {}} />);
    expect(screen.getByText('Release required to activate merged work')).toBeInTheDocument();
    expect(screen.queryByText('Release scope')).not.toBeInTheDocument();
  });

  it('shows fail-closed unavailability explicitly', () => {
    render(<ReleasePopover anchor={anchor()} readiness={readiness} releaseCheck={null} onClose={() => {}} />);
    expect(screen.getByText('BLOCKED — release services unavailable — releases fail closed')).toBeInTheDocument();
    expect(screen.getByText('Release services unavailable — releases fail closed.')).toBeInTheDocument();
  });

  it('surfaces a non-task blocking reason and expands its details', () => {
    render(<ReleasePopover anchor={anchor()} readiness={readiness} releaseCheck={check({
      blockers: [], warnings: ['release cut source moved during the build'],
    })} onClose={() => {}} />);
    expect(screen.getByText('BLOCKED — release cut source moved during the build')).toBeInTheDocument();
    expect(screen.getByText('release cut source moved during the build')).toBeVisible();
  });

  it('renders the declared release train with a working veto', () => {
    const onVetoTrain = vi.fn();
    render(<ReleasePopover
      anchor={anchor()}
      readiness={readiness}
      train={train()}
      onVetoTrain={onVetoTrain}
      onClose={() => {}}
    />);
    const dialog = screen.getByRole('dialog', { name: 'Release readiness' });
    expect(within(dialog).getByText(/Release train v0\.0\.991 departs/)).toBeInTheDocument();
    expect(within(dialog).getByText(/veto window 10 min/)).toBeInTheDocument();
    expect(within(dialog).queryByText('Next auto release')).not.toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Veto' }));
    expect(onVetoTrain).toHaveBeenCalledTimes(1);
    expect(within(dialog).getByText(/a veto only parks the announced train/)).toBeInTheDocument();
  });

  it('renders a parked train reason instead of a departure row', () => {
    render(<ReleasePopover anchor={anchor()} readiness={readiness} train={train({
      publishState: 'idle',
      state: { phase: 'parked', train: null, parked: { reason: 'vetoed by tasks-ui', head: 'abc', at: '2026-08-15T12:05:00Z' } },
    })} onClose={() => {}} />);
    expect(screen.getByText('Release train parked: vetoed by tasks-ui')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Veto' })).not.toBeInTheDocument();
  });

  it('makes a failed publish the headline even when the release scope is safe', () => {
    render(<ReleasePopover
      anchor={anchor()}
      readiness={readiness}
      releaseCheck={check({ allowed: true, blockers: [] })}
      train={train({
        publishState: 'failed',
        state: {
          phase: 'parked',
          train: { id: 'rtr_failed', version: '0.0.991', status: 'parked', declaredAt: '2026-08-15T12:00:00Z', departAt: '2026-08-15T12:10:00Z' },
          parked: {
            reason: 'publish failed', at: '2026-08-15T12:20:00Z', failureClass: 'package_timeout',
            attempt: 0, maxAttempts: 1, retryEligible: true, nextRetryAt: '2026-08-15T12:35:00Z',
          },
        },
      })}
      onClose={() => {}}
    />);
    const headline = screen.getByText('Publish failed · package timeout');
    expect(headline).toHaveClass('vp-verdict-warn');
    expect(screen.getByText('Scope safe · v0.0.991 · source 0123456')).toBeInTheDocument();
    expect(screen.queryByText(/Safe to publish/)).not.toBeInTheDocument();
  });

  it('shows package timeout retry class, count, eligibility and next time', () => {
    render(<ReleasePopover anchor={anchor()} readiness={readiness} train={train({
      publishState: 'failed',
      state: {
        phase: 'parked', train: null,
        parked: {
          reason: 'publish failed', at: '2026-08-15T12:20:00Z', failureClass: 'package_timeout',
          evidence: 'FAIL soluterminal/daemon 600.187s', attempt: 0, maxAttempts: 1,
          retryEligible: true, nextRetryAt: '2026-08-15T12:35:00Z',
        },
      },
    })} onClose={() => {}} />);
    expect(screen.getAllByText('package timeout').length).toBeGreaterThan(0);
    expect(screen.getByText(/retry 0\/1 · eligible/)).toBeInTheDocument();
    expect(screen.getByText(/auto-retry at/)).toBeInTheDocument();
    expect(screen.getByText('FAIL soluterminal/daemon 600.187s')).toBeInTheDocument();
  });

  it('keeps a deterministic assertion failure parked for a fixing commit', () => {
    render(<ReleasePopover anchor={anchor()} readiness={readiness} train={train({
      publishState: 'failed',
      state: {
        phase: 'parked', train: null,
        parked: {
          reason: 'tests failed', at: '2026-08-15T12:20:00Z', failureClass: 'test_assertion',
          attempt: 0, maxAttempts: 1, retryEligible: false,
        },
      },
    })} onClose={() => {}} />);
    expect(screen.getAllByText('test failure').length).toBeGreaterThan(0);
    expect(screen.getByText('parked until a fixing commit lands')).toBeInTheDocument();
    expect(screen.queryByText(/auto-retry/)).not.toBeInTheDocument();
  });

  it('renders retrying and published as distinct publish states', () => {
    const { unmount } = render(<ReleasePopover anchor={anchor()} readiness={readiness} train={train({
      publishState: 'retrying',
      state: {
        phase: 'parked',
        train: { id: 'rtr_retry', version: '0.0.991', status: 'parked', declaredAt: '2026-08-15T12:00:00Z', departAt: '2026-08-15T12:10:00Z' },
        parked: { reason: 'timeout', at: '2026-08-15T12:20:00Z', attempt: 1, maxAttempts: 1 },
      },
    })} onClose={() => {}} />);
    expect(screen.getByText('retrying')).toBeInTheDocument();
    expect(screen.getByText(/full test gate running/)).toBeInTheDocument();
    unmount();

    render(<ReleasePopover anchor={anchor()} readiness={readiness} train={train({
      publishState: 'published',
      state: {
        phase: 'idle',
        train: { id: 'rtr_done', version: '0.0.991', sourceSha: 'abcdef012345', status: 'published', declaredAt: '2026-08-15T12:00:00Z', departAt: '2026-08-15T12:10:00Z' },
      },
    })} onClose={() => {}} />);
    expect(screen.getByText('Published · v0.0.991')).toHaveClass('release-verdict-safe');
    expect(screen.getByText('published')).toBeInTheDocument();
  });

  it('renders a published train whose supervision was lost at restart distinctly from a failure', () => {
    const note = 'publish completed; supervision lost at daemon restart — reconciled at boot from release ledger records v0.0.991 from a7732cb';
    render(<ReleasePopover anchor={anchor()} readiness={readiness} train={train({
      publishState: 'published',
      state: {
        phase: 'idle',
        train: {
          id: 'rtr_unsupervised', version: '0.0.991', status: 'published',
          sourceSha: 'a7732cb0123456789', supervisionLost: true, publishNote: note,
          declaredAt: '2026-08-15T12:00:00Z', departAt: '2026-08-15T12:10:00Z',
        },
      },
    })} onClose={() => {}} />);
    expect(screen.getByText('Published · v0.0.991 · supervision lost at restart')).toHaveClass('release-verdict-safe');
    expect(screen.getByText('supervision lost at restart')).toBeInTheDocument();
    expect(screen.getByText(note)).toBeInTheDocument();
    expect(screen.queryByText(/Publish failed/)).toBeNull();
  });

  it('keeps the forecast beside a published headline and hides it during an active publish', () => {
    const releaseAnchor = anchor();
    const completedTrain = {
      id: 'rtr_done', version: '0.0.991', sourceSha: 'abcdef012345', status: 'published',
      declaredAt: '2026-08-15T12:00:00Z', departAt: '2026-08-15T12:10:00Z',
    };
    const view = render(<ReleasePopover
      anchor={releaseAnchor}
      readiness={readiness}
      train={train({
        publishState: 'published',
        state: { phase: 'idle', train: completedTrain },
        preview: preview(),
      })}
      onClose={() => {}}
    />);
    expect(screen.getByText('Published · v0.0.991')).toBeInTheDocument();
    expect(screen.getByText('Next auto release')).toBeInTheDocument();

    view.rerender(<ReleasePopover
      anchor={releaseAnchor}
      readiness={readiness}
      train={train({
        publishState: 'testing',
        state: { phase: 'publishing', train: completedTrain },
        preview: preview(),
      })}
      onClose={() => {}}
    />);
    expect(screen.queryByText('Next auto release')).not.toBeInTheDocument();
    expect(screen.getByText(/full test gate running/)).toBeInTheDocument();
  });

  it('accepts an old-daemon parked payload without publish fields', () => {
    const legacy = train({
      state: { phase: 'parked', train: null, parked: { reason: 'legacy failure', at: '2026-08-15T12:05:00Z' } },
    });
    delete legacy.publishState;
    expect(() => render(<ReleasePopover anchor={anchor()} readiness={readiness} train={legacy} onClose={() => {}} />)).not.toThrow();
    expect(screen.getByText('Release train parked: legacy failure')).toBeInTheDocument();
  });

  it('renders no train row when the daemon reports none', () => {
    render(<ReleasePopover anchor={anchor()} readiness={readiness} releaseCheck={check()} onClose={() => {}} />);
    expect(screen.queryByText(/Release train/)).not.toBeInTheDocument();
    expect(screen.getByText('Informational only — nothing here publishes or approves a release.')).toBeInTheDocument();
  });

  it('forecasts both auto-release triggers with the age ETA when the train is not eligible', () => {
    render(<ReleasePopover
      anchor={anchor()}
      readiness={readiness}
      releaseCheck={check({ allowed: true, blockers: [], warnings: ['w1', 'w2'] })}
      train={train({
        state: { phase: 'idle', train: null, parked: null, restartPending: null },
        preview: preview(),
      })}
      onClose={() => {}}
    />);
    const dialog = screen.getByRole('dialog', { name: 'Release readiness' });
    expect(within(dialog).getByText('Next auto release')).toBeInTheDocument();
    const eta = dialog.querySelector('.release-forecast-eta');
    expect(eta).toHaveTextContent(/After 3 more functional landings \(3 of 6\), or at/);
    expect(eta).toHaveTextContent(/\(2\.8h of 12h\) — whichever first/);
    expect(eta?.querySelector('time')).toHaveAttribute('datetime', '2026-08-15T21:12:00Z');
    expect(within(dialog).getByText('3 of 6 functional landings and oldest 2.8h of 12h')).toBeInTheDocument();
    const meta = dialog.querySelector('.release-forecast-meta');
    expect(meta).toHaveTextContent(/10-min veto notice before departure/);
    expect(meta).toHaveTextContent(/no blockers, 2 advisories/);
    expect(within(dialog).queryByRole('button', { name: 'Veto' })).not.toBeInTheDocument();
    expect(within(dialog).getByText('Informational only — nothing here publishes or approves a release.')).toBeInTheDocument();
  });

  it('states the departure time and veto window when the train is eligible', () => {
    render(<ReleasePopover
      anchor={anchor()}
      readiness={readiness}
      releaseCheck={check({ allowed: true, blockers: [] })}
      train={train({
        state: { phase: 'idle', train: null, parked: null, restartPending: null },
        preview: preview({
          eligible: true,
          reasons: [],
          functionalLandings: 6,
          landingsRemaining: 0,
          wouldDepartAt: '2026-08-15T12:10:00Z',
        }),
      })}
      onClose={() => {}}
    />);
    const dialog = screen.getByRole('dialog', { name: 'Release readiness' });
    const eta = dialog.querySelector('.release-forecast-eta');
    expect(eta).toHaveTextContent(/Eligible now — departs at/);
    expect(eta?.querySelector('time')).toHaveAttribute('datetime', '2026-08-15T12:10:00Z');
    expect(eta).toHaveTextContent(/after a 10-min veto notice/);
    expect(within(dialog).queryByText(/whichever first/)).not.toBeInTheDocument();
  });

  it('explains a disabled train as off using the daemon reason verbatim', () => {
    const disabled = train({
      policy: {
        enabled: false, minFunctionalLandings: 6, maxUnreleasedAgeHours: 12,
        quietRequired: true, notifyVetoMinutes: 10,
      },
      state: { phase: 'disabled', train: null, parked: null, restartPending: null },
      preview: preview({ reasons: ['release train policy is disabled'], functionalLandings: 0 }),
    });
    render(<ReleasePopover anchor={anchor()} readiness={readiness} train={disabled} onClose={() => {}} />);
    expect(screen.getByText(/Off — release train policy is disabled/)).toBeInTheDocument();
  });

  it('summarises blockers and fail-closed release-check unavailability in the forecast meta', () => {
    const idle = train({
      state: { phase: 'idle', train: null, parked: null, restartPending: null },
      preview: preview(),
    });
    const view = render(<ReleasePopover
      anchor={anchor()} readiness={readiness} releaseCheck={check()} train={idle} onClose={() => {}}
    />);
    expect(document.querySelector('.release-forecast-meta')).toHaveTextContent(/1 blocker\(s\), 0 advisories/);
    view.rerender(<ReleasePopover
      anchor={anchor()} readiness={readiness} releaseCheck={null} train={idle} onClose={() => {}}
    />);
    expect(document.querySelector('.release-forecast-meta')).toHaveTextContent(/release check: unavailable \(fails closed\)/);
  });

  it('labels landings separately from files', () => {
    render(<ReleasePopover
      anchor={anchor()}
      readiness={readiness}
      train={train({
        state: { phase: 'idle', train: null, parked: null, restartPending: null },
        preview: preview(),
      })}
      onClose={() => {}}
    />);
    expect(screen.getByText(/3 functional landings count toward auto release/)).toBeInTheDocument();
    expect(screen.getByText(/3 functional files \+ 1 documentation files changed/)).toBeInTheDocument();
  });

  it('says nothing about auto release while the release train is off', () => {
    const off = train({
      state: { phase: 'disabled', train: null, parked: null, restartPending: null },
      preview: preview(),
    });
    off.policy = { ...off.policy, enabled: false };
    render(<ReleasePopover anchor={anchor()} readiness={readiness} train={off} onClose={() => {}} />);
    expect(screen.queryByText(/count toward auto release/)).toBeNull();
    expect(screen.getByText(/3 functional files \+ 1 documentation files changed/)).toBeInTheDocument();
  });

  it('leads with the actual hold reason when both release triggers are already satisfied', () => {
    const reason = 'release train requires a quiet engine (1 running, 0 pending, 1 awaiting approval)';
    render(<ReleasePopover
      anchor={anchor()}
      readiness={readiness}
      train={train({
        policy: {
          enabled: true, minFunctionalLandings: 6, maxUnreleasedAgeHours: 12,
          quietRequired: true, notifyVetoMinutes: 10,
        },
        state: { phase: 'idle', train: null, parked: null, restartPending: null },
        preview: preview({
          evaluatedAt: '2026-08-15T12:00:00Z',
          functionalLandings: 7,
          landingsRemaining: 0,
          ageHours: 14,
          ageTriggerAt: '2026-08-15T10:00:00Z',
          engine: { running: 1, pending: 0, awaitingApproval: 1, quiet: false },
          reasons: [reason],
        }),
      })}
      onClose={() => {}}
    />);
    const eta = document.querySelector('.release-forecast-eta');
    expect(eta).toHaveTextContent(`Held — ${reason}`);
    expect(eta?.querySelector('time')).not.toBeInTheDocument();
    expect(eta).not.toHaveTextContent(/14\.0h of 12h/);
    expect(screen.getByRole('list', { name: 'Waiting on' })).toHaveTextContent(reason);
  });

  it.each([
    ['an array', []],
    ['an object without state', {}],
    ['an object with null state', { state: null }],
  ])('ignores malformed train payloads shaped as %s', (_description, malformedTrain) => {
    expect(() => render(<ReleasePopover
      anchor={anchor()}
      readiness={readiness}
      train={malformedTrain as unknown as ReleaseTrainStatus}
      onClose={() => {}}
    />)).not.toThrow();
    expect(screen.queryByText(/Release train/)).not.toBeInTheDocument();
    expect(screen.getByText('Informational only — nothing here publishes or approves a release.')).toBeInTheDocument();
  });

  it('renders inferred provenance for a scoped legacy anchor', () => {
    render(<ReleasePopover anchor={anchor()} readiness={{ ...readiness, anchorProvenance: 'inferred' }} releaseCheck={check()} onClose={() => {}} />);
    const badge = screen.getByText('inferred provenance');
    expect(badge).toBeInTheDocument();
    expect(badge).toHaveClass('is-inferred');
  });

  it('explains a degraded anchor instead of showing a silently wrong delta', () => {
    const note = 'update manifest fetch failing: 503';
    render(<ReleasePopover
      anchor={anchor()}
      readiness={{ ...readiness, anchorProvenance: 'inferred', anchorNote: note }}
      releaseCheck={check()}
      onClose={() => {}}
    />);
    expect(screen.getByText(note)).toBeInTheDocument();
  });

  // An anchor can be exact AND degraded: a manifest the daemon still has but
  // can no longer refresh anchors correctly while already being a release
  // behind. Tying the note to inferred provenance would hide exactly that case.
  it('explains a degraded anchor that is still exact', () => {
    const note = 'anchor from the last good manifest; update manifest fetch failing: 503';
    render(<ReleasePopover
      anchor={anchor()}
      readiness={{ ...readiness, anchorProvenance: 'exact', anchorNote: note }}
      releaseCheck={check()}
      onClose={() => {}}
    />);
    expect(screen.getByText(note)).toBeInTheDocument();
    expect(screen.getByText('exact provenance')).toBeInTheDocument();
  });

  it('renders no anchor note when the anchor is clean', () => {
    render(<ReleasePopover anchor={anchor()} readiness={readiness} releaseCheck={check()} onClose={() => {}} />);
    expect(screen.queryByText(/update manifest/)).not.toBeInTheDocument();
    expect(screen.getByText('exact provenance')).toBeInTheDocument();
  });
});
