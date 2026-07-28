package controller

import (
	"fmt"
	"strings"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const (
	modeRecord = "record"
	modeReplay = "replay"
)

// operatingMode returns record or replay (empty defaults to record).
func operatingMode(st *enginev1alpha1.ShadowTest) string {
	m := strings.TrimSpace(strings.ToLower(st.Spec.Mode))
	if m == "" {
		return modeRecord
	}
	return m
}

// validateStorage checks required spec.storage (BYOB S3) and mode/session rules.
func validateStorage(st *enginev1alpha1.ShadowTest) error {
	mode := operatingMode(st)
	switch mode {
	case modeRecord, modeReplay:
	default:
		return fmt.Errorf("spec.mode %q is not supported (want record or replay)", st.Spec.Mode)
	}

	cfg := st.Spec.Storage
	if cfg == nil {
		return fmt.Errorf("spec.storage is required")
	}
	if strings.TrimSpace(cfg.BucketName) == "" {
		return fmt.Errorf("storage.bucketName is required")
	}
	typ := strings.TrimSpace(strings.ToLower(cfg.Type))
	if typ == "" {
		return fmt.Errorf("storage.type is required")
	}
	if typ != "s3" {
		return fmt.Errorf("storage.type %q is not supported (want s3)", cfg.Type)
	}
	if p := strings.TrimSpace(cfg.RetentionPolicy); p != "" {
		switch p {
		case "Retain", "Delete":
		default:
			return fmt.Errorf("storage.retentionPolicy %q is not supported (want Retain or Delete)", cfg.RetentionPolicy)
		}
	}
	if cfg.CredentialsSecretRef != nil && strings.TrimSpace(cfg.CredentialsSecretRef.Name) == "" {
		return fmt.Errorf("storage.credentialsSecretRef.name is required when credentialsSecretRef is set")
	}

	if mode == modeReplay {
		sid := strings.TrimSpace(st.Spec.SessionID)
		if sid == "" {
			sid = strings.TrimSpace(st.Status.CurrentSessionID)
		}
		if sid == "" {
			return fmt.Errorf("replay mode requires spec.sessionID or status.currentSessionID")
		}
	}
	return nil
}
