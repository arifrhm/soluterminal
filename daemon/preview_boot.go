package main

import (
	"context"
	"log"
)

// reconcilePreviewsAtBoot expires adopted previews whose process is gone and
// retries pending cloud revokes. It runs once this process owns the local
// port, so a losing twin never revokes cloud rows.
func (a *apiServer) reconcilePreviewsAtBoot(ctx context.Context) {
	if a.previews == nil {
		return
	}
	s := a.previews.reconcileBoot(ctx)
	log.Printf("previews: boot reconcile kept=%d expiredAdopt=%d expiredUnverifiable=%d revoked=%d pendingRevoke=%d",
		s.Kept, s.ExpiredAdopt, s.ExpiredUnverifiable, s.Revoked, s.PendingRevoke)
}
