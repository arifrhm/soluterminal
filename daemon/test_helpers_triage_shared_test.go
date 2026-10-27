package main

// museBillingLine is the 2026-08-18 incident's terminal reason, verbatim
// from wft_99ab4fcc logs/inv_f0c4ed.log (run.terminal.failed).
const museBillingLine = "API error 402 [request_id=e40f9f4b-5e75-41c8-a9d1-060690bc6856]: Billing verification failed. Please check your payment method. (billing_error) (after 10 provider attempts) (request id: 3aff6dd6-1d7c-4eec-b296-4b0a76b3f198)"
const museSubscriptionQuotaLine = "API error 429 [request_id=1f540f9f-5e75-41c8-a9d1-060690bc6856]: Subscription quota exhausted. Your usage window resets at 2026-09-07T00:00:00Z. (rate_limit_error)"
