package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
	"github.com/shadow-diff/s3utils"
)

// cleanupS3IfNeeded deletes objects under shadow-diff/<ns>/<name>/ when
// retentionPolicy is Delete. Never deletes the BYOB bucket itself.
func (r *ShadowTestReconciler) cleanupS3IfNeeded(ctx context.Context, st *enginev1alpha1.ShadowTest) error {
	if !controllerutil.ContainsFinalizer(st, s3Finalizer) {
		return nil
	}

	log := logf.FromContext(ctx)
	policy := ""
	if st.Spec.Storage != nil {
		policy = strings.TrimSpace(st.Spec.Storage.RetentionPolicy)
	}
	if policy == "" || policy == "Retain" {
		log.Info("S3 retention policy is Retain. Skipping bucket cleanup",
			"shadowtest", fmt.Sprintf("%s/%s", st.Namespace, st.Name))
		return nil
	}
	if policy != "Delete" {
		log.Info("S3 retention policy is Retain. Skipping bucket cleanup",
			"retentionPolicy", policy)
		return nil
	}

	cleaner := r.S3Cleaner
	if cleaner == nil {
		cleaner = r.defaultS3Cleaner
	}
	prefix := s3utils.TestKeyPrefix(st.Namespace, st.Name)
	if err := cleaner(ctx, st); err != nil {
		return err
	}
	log.Info(fmt.Sprintf("S3 directory %s recursively deleted", prefix),
		"prefix", prefix,
		"bucket", st.Spec.Storage.BucketName,
	)
	return nil
}

func (r *ShadowTestReconciler) defaultS3Cleaner(ctx context.Context, st *enginev1alpha1.ShadowTest) error {
	cfg, err := r.s3ConfigFromShadowTest(ctx, st)
	if err != nil {
		return err
	}
	prefix := s3utils.TestKeyPrefix(st.Namespace, st.Name)
	return s3utils.DeletePrefix(ctx, cfg, prefix)
}

func (r *ShadowTestReconciler) s3ConfigFromShadowTest(ctx context.Context, st *enginev1alpha1.ShadowTest) (s3utils.Config, error) {
	storage := st.Spec.Storage
	if storage == nil {
		return s3utils.Config{}, fmt.Errorf("spec.storage is required for S3 cleanup")
	}
	cfg := s3utils.Config{
		Bucket:   strings.TrimSpace(storage.BucketName),
		Endpoint: strings.TrimSpace(storage.Endpoint),
		Region:   strings.TrimSpace(storage.Region),
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if storage.CredentialsSecretRef != nil {
		name := strings.TrimSpace(storage.CredentialsSecretRef.Name)
		if name != "" {
			var sec corev1.Secret
			if err := r.Get(ctx, client.ObjectKey{Namespace: st.Namespace, Name: name}, &sec); err != nil {
				return s3utils.Config{}, fmt.Errorf("storage credentials secret %s/%s: %w", st.Namespace, name, err)
			}
			cfg.AccessKeyID = string(sec.Data[secretKeyAccess])
			cfg.SecretAccessKey = string(sec.Data[secretKeySecret])
		}
	}
	if cfg.Bucket == "" {
		return s3utils.Config{}, fmt.Errorf("storage.bucketName is required for S3 cleanup")
	}
	return cfg, nil
}
