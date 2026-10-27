package main

import (
	"regexp"
)

// reasonCodeFields is the stable machine-readable half of a stop reason.
// Embedded by value so JSON flattens to sibling keys with omitempty (same
// additive convention as the stage events' TimeoutKind). Older readers decode
// identically; newer fields are additive and omitempty.
type reasonCodeFields struct {
	Code           string `json:"code,omitempty"`
	AuthorityClass string `json:"authorityClass,omitempty"`
	Retry          string `json:"retry,omitempty"`
	Reversibility  string `json:"reversibility,omitempty"`
}

const (
	authorityHumanOnly     = "human_only"
	authorityConfigurable  = "configurable"
	authoritySafetyBound   = "safety_bound"
	authorityExternalWait  = "external_wait"
	authorityOperatorPause = "operator_pause"
	authorityEngineWait    = "engine_wait"
	authorityAutomationGap = "automation_gap"
	authorityUnknown       = "unknown"
)

const (
	retryAuto   = "auto"
	retryManual = "manual"
	retryNone   = "none"
)

const (
	reversible   = "reversible"
	irreversible = "irreversible"
)

type reasonEntry struct {
	AuthorityClass string
	Retry          string
	Reversibility  string
	Text           string
}

const (
	codeTaskMissing                         = "task_missing"
	codeNotAwaitingApproval                 = "not_awaiting_approval"
	codePlanGateRequiresHuman               = "plan_gate_requires_human"
	codeTemplateDecisionRequiresHuman       = "template_decision_requires_human"
	codeTemplateSecurityReviewRequiresHuman = "template_security_review_requires_human"
	codeFrozenTemplateVariantUnavailable    = "frozen_template_variant_unavailable"
	codeDeliverableNotChange                = "deliverable_not_change"
	codeCriticalTierFloor                   = "critical_tier_floor"
	codeRiskFloor                           = "risk_floor"
	codeRevertRequiresHuman                 = "revert_requires_human"
	codeRecoverySuccessorRequiresHuman      = "recovery_successor_requires_human"
	codeScreensMissing                      = "screens_missing"
	codeBrowserEvidenceMissing              = "browser_evidence_missing"
	codeBrowserEvidenceStale                = "browser_evidence_stale"
	codeBrowserRed                          = "browser_red"
	codeBrowserInfraUnavailable             = "browser_infra_unavailable"
)

const (
	codeBrowserInfraAcceptDisabled            = "browser_infra_accept_disabled"
	codeBrowserInfraRuntimeMissing            = "browser_infra_runtime_missing"
	codeBrowserInfraVersionSkew               = "browser_infra_version_skew"
	codeBrowserInfraPermissionDenied          = "browser_infra_permission_denied"
	codeBrowserInfraPreviewBackendUnavailable = "browser_infra_preview_backend_unavailable"
	codeBrowserInfraAppNotServed              = "browser_infra_app_not_served"
	codeBrowserInfraDriverNotAttempted        = "browser_infra_driver_not_attempted"
	codeBrowserInfraDriverIncomplete          = "browser_infra_driver_incomplete"
	codeBrowserInfraPortConflict              = "browser_infra_port_conflict"
	codeBrowserInfraSandboxUnavailable        = "browser_infra_sandbox_unavailable"
	codeBrowserInfraIdentityUnavailable       = "browser_infra_identity_unavailable"
	codeBrowserInfraLeaseActive               = "browser_infra_lease_active"
	codeBrowserInfraCreateUncertain           = "browser_infra_create_uncertain"
	codeBrowserInfraCleanupPending            = "browser_infra_cleanup_pending"
	codeBrowserInfraDaemonInternal            = "browser_infra_daemon_internal"
	codeBrowserInfraUnclassified              = "browser_infra_unclassified"
	codeBrowserDriverNoOutput                 = "browser_driver_no_output"
	codeBrowserDriverExecutionFailed          = "browser_driver_execution_failed"
	codeBrowserBindingMismatch                = "browser_binding_mismatch"
)

const (
	codeBrowserYellow                   = "browser_yellow"
	codeBrowserGreenPolicyRequiresHuman = "browser_green_policy_requires_human"
	codeBrowserVerdictUnknown           = "browser_verdict_unknown"
	codeBrowserAttention                = "browser_attention"
	codeVerificationNotGreen            = "verification_not_green"
	codeRequiredVerificationNotRun      = "required_verification_not_run"
	codeReviewNotRun                    = "review_not_run"
	codeReviewGatingFindings            = "review_gating_findings"
	codeDiffEmpty                       = "diff_empty"
	codeBinaryDiff                      = "binary_diff"
	codeAutopilotAuthorityPath          = "autopilot_authority_path"
	codeProtectedPath                   = "protected_path"
	codeEngineControlPath               = "engine_control_path"
	codeEngineControlPathConditionUnmet = "engine_control_path_condition_unmet"
	codeAutopilotLevelZero              = "autopilot_level_zero"
	codeRepoOptedOut                    = "repo_opted_out"
	codeSupervisedApprovalNotConsented  = "supervised_approval_not_consented"
	codeDiffExceedsBounds               = "diff_exceeds_bounds"
	codePreauthorizationOff             = "preauthorization_off"
	codeDocsOnlyExceedsBounds           = "docs_only_exceeds_bounds"
	codeNoClassMatches                  = "no_class_matches"
	codeGateGrantAbsent                 = "gate_grant_absent"
	codeGateNoPolicies                  = "gate_no_policies"
	codeJudgeHumanReview                = "judge_human_review"
	codeJudgeRequiredGate               = "judge_required_gate"
	codeRegatedAfterMaterialChange      = "regated_after_material_change"
	codeApprovalBriefMissing            = "approval_brief_missing"
	codeApprovalBriefOpenQuestions      = "approval_brief_open_questions"
	codeSpecMismatch                    = "spec_mismatch"
	codeRepairRoundsExceedPolicy        = "repair_rounds_exceed_policy"
	codeGatePolicyScopeMismatch         = "gate_policy_scope_mismatch"
	codeGatePolicyDisappeared           = "gate_policy_disappeared"
	codeApprovalUnevaluated             = "approval_unevaluated"
	codeApprovalCarryRefused            = "approval_carry_refused"

	codePreauthVetoWindow           = "preauth_veto_window"
	codePreauthHourlyLimitDeferred  = "preauth_hourly_limit_deferred"
	codePreauthHourlyLimitParked    = "preauth_hourly_limit_parked"
	codePreauthFleetImpaired        = "preauth_fleet_impaired"
	codePreauthDaemonRestart        = "preauth_daemon_restart"
	codePreauthClassChanged         = "preauth_class_changed"
	codePreauthInputsUnavailable    = "preauth_inputs_unavailable"
	codePreauthCircuitBreaker       = "preauth_circuit_breaker"
	codePreauthKillSwitch           = "preauth_kill_switch"
	codePreauthHumanVeto            = "preauth_human_veto"
	codePreauthSupersededByHuman    = "preauth_superseded_by_human"
	codePreauthTaskCancelledByHuman = "preauth_task_cancelled_by_human"

	codeQuotaExhausted            = "quota_exhausted"
	codeAuthLapsed                = "auth_lapsed"
	codeBillingBlocked            = "billing_blocked"
	codeBudgetCeiling             = "budget_ceiling"
	codeEnvironmentBlocked        = "environment_blocked"
	codeModelPolicyRouteExhausted = "model_policy_route_exhausted"
	codeFallbackConsentPending    = "fallback_consent_pending"
	codeCollaborationAttention    = "collaboration_attention"

	codeLandRebaseConflict         = "land_rebase_conflict"
	codeLandReverifyFailed         = "land_reverify_failed"
	codeLandNoChecks               = "land_no_checks"
	codeLandBrowserAcceptStale     = "land_browser_accept_stale"
	codeLandHeadChurn              = "land_head_churn"
	codeLandNotRunning             = "land_not_running"
	codeLandAssertionFailed        = "land_assertion_failed"
	codeLandAssertionUnverified    = "land_assertion_unverified"
	codeLandDirtyTree              = "land_dirty_tree"
	codeLandNeedsMergeUnclassified = "land_needs_merge_unclassified"
	codeLandHeldRetrying           = "land_held_retrying"

	codeDependencyHoldDepends  = "dependency_hold_depends"
	codeDependencyHoldDeferred = "dependency_hold_deferred"
	codeDependencyHoldLand     = "dependency_hold_land"
	codeDependencyHoldOverlap  = "dependency_hold_overlap"
	codeDependencyDeadBlocker  = "dependency_dead_blocker"
	codeCapacityQueued         = "capacity_queued"
	codeLandingWait            = "landing_wait"
	codeResourceWait           = "resource_wait"
	codeVerifyQueued           = "verify_queued"

	codeProposalNotPending                = "proposal_not_pending"
	codeProposalNeedsHumanJudgment        = "proposal_needs_human_judgment"
	codeProposalTriageRecommendsHuman     = "proposal_triage_recommends_human"
	codeProposalKindNotAllowlisted        = "proposal_kind_not_allowlisted"
	codeProposalValidationFailed          = "proposal_validation_failed"
	codeProposalHourlyCap                 = "proposal_hourly_cap"
	codeProposalAutopilotDisabled         = "proposal_autopilot_disabled"
	codeProposalExecutionValidationFailed = "proposal_execution_validation_failed"
	codeProposalHumanVeto                 = "proposal_human_veto"
	codeProposalHumanDecided              = "proposal_human_decided"

	codeTriageRecommendsHuman             = "triage_recommends_human"
	codeTriageActionNotAutoApplicable     = "triage_action_not_auto_applicable"
	codeAutoApplyAttemptsExhausted        = "autoapply_attempts_exhausted"
	codeAutoApplyRetryLineageUsed         = "autoapply_retry_lineage_used"
	codeAutoApplyRecommendationUnreadable = "autoapply_recommendation_unreadable"
	codeAutoApplyConfidenceBelowThreshold = "autoapply_confidence_below_threshold"
	codeAutoApplyPreconditionUnmet        = "autoapply_precondition_unmet"
	codeAutoApplyActionUnregistered       = "autoapply_action_unregistered"
	codeAutoApplyRelandPreconditionUnmet  = "autoapply_reland_precondition_unmet"
	codeAutoApplyWorktreesUnavailable     = "autoapply_worktrees_unavailable"
	codeAutoApplyWorktreeMissing          = "autoapply_worktree_missing"
	codeAutoApplyBranchMissing            = "autoapply_branch_missing"
	codeAutoApplyTrialMergeError          = "autoapply_trial_merge_error"
	codeAutoApplyTrialMergeConflict       = "autoapply_trial_merge_conflict"
	codeAutoApplyResumeLimitExhausted     = "autoapply_resume_limit_exhausted"
	codeAutoApplyStartFailed              = "autoapply_start_failed"
	codeAutoApplyHumanEscalationOpen      = "autoapply_human_escalation_open"
	codeContentSafetyStop                 = "content_safety_stop"
	codeContentSafetyBudgetUnreadable     = "content_safety_budget_unreadable"
	codeContentSafetyRepoCap              = "content_safety_repo_cap"
	codeRecoveryEscalatedUnclassified     = "recovery_escalated_unclassified"

	codeNoProgressIdle      = "no_progress_idle"
	codeNoProgressHard      = "no_progress_hard"
	codeTimeoutUnclassified = "timeout_unclassified"

	codeAutonomyGlobalPaused  = "autonomy_global_paused"
	codeAutonomyRowPaused     = "autonomy_row_paused"
	codeControllerOff         = "controller_off"
	codeAgentPaused           = "agent_paused"
	codeAgentRuntimeBlocked   = "agent_runtime_blocked"
	codeAgentQuotaPaused      = "agent_quota_paused"
	codeAgentCredentialPaused = "agent_credential_paused"

	codeLegacyUnmapped = "legacy_unmapped"
)

const (
	codeOwnerOverrideDisabled       = "owner_override_disabled"
	codeOwnerNotFullAutomation      = "owner_not_full_automation"
	codeOwnerAuthorityUnavailable   = "owner_authority_unavailable"
	codeOwnerBindingMissing         = "owner_binding_missing"
	codeOwnerEvidenceChanged        = "owner_evidence_changed"
	codeOwnerVerificationIncomplete = "owner_verification_incomplete"
	codeOwnerConsentMismatch        = "owner_consent_scope_mismatch"
	codeOwnerRateLimited            = "owner_approval_rate_limited"
)

func reasonFields(code string) reasonCodeFields {
	if entry, ok := reasonCatalog[code]; ok {
		return reasonCodeFields{Code: code, AuthorityClass: entry.AuthorityClass, Retry: entry.Retry, Reversibility: entry.Reversibility}
	}
	// unregistered code returns legacy_unmapped
	fallback := reasonCatalog[codeLegacyUnmapped]
	return reasonCodeFields{Code: codeLegacyUnmapped, AuthorityClass: fallback.AuthorityClass, Retry: fallback.Retry, Reversibility: fallback.Reversibility}
}

var (
	legacyBrowserRedRE     = regexp.MustCompile(`^browser acceptance is red: `)
	legacyBrowserInfraRE   = regexp.MustCompile(`^browser acceptance infrastructure was unavailable: `)
	legacyBrowserYellowRE  = regexp.MustCompile(`^browser acceptance needs human attention: `)
	legacyGatingFindingsRE = regexp.MustCompile(`^\d+ gating finding\(s\)$`)
	legacyOpenQuestionsRE  = regexp.MustCompile(`^approval brief has \d+ open question\(s\)$`)
)

const (
	planePark       = "park"
	planeApproval   = "approval"
	planeLand       = "land"
	planeDependency = "dependency"
	planeProposal   = "proposal"
	planeTriage     = "triage"
	planeRecovery   = "recovery"
	planeFailure    = "failure"
)
