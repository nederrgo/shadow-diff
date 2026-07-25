package reconcile

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	enginesv1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
	"github.com/shadow-diff/pixie-gate/internal/export"
	"github.com/shadow-diff/pixie-gate/internal/pxl"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Gate reconciles PixieStreamRule CRs → rendered PxL → px run → status patch.
type Gate struct {
	Client   client.Client
	Loader   pxl.Loader
	Runner   *export.Runner
	StateDir string
}

// ReconcileOnce lists all rules and exports each in parallel (bash bridge parity).
func (g *Gate) ReconcileOnce(ctx context.Context) error {
	var list enginesv1alpha1.PixieStreamRuleList
	if err := g.Client.List(ctx, &list); err != nil {
		return fmt.Errorf("list pixiestreamrules: %w", err)
	}
	var wg sync.WaitGroup
	for i := range list.Items {
		wg.Add(1)
		go func(rule *enginesv1alpha1.PixieStreamRule) {
			defer wg.Done()
			g.exportRule(ctx, rule)
		}(&list.Items[i])
	}
	wg.Wait()
	return nil
}

func (g *Gate) exportRule(ctx context.Context, rule *enginesv1alpha1.PixieStreamRule) {
	ns, name := rule.Namespace, rule.Name
	prefix := filepath.Join(g.StateDir, ns+"-"+name)

	if !rule.Spec.Active {
		removeGlob(prefix + "*.pxl")
		g.patchStatus(ctx, rule, "Inactive", "spec.active=false")
		return
	}

	var failed []string
	ok := false

	if rule.Spec.OTelEndpoint != "" {
		path := prefix + "-ingress.pxl"
		if err := g.Loader.WriteFile(pxl.KindIngress, rule.Spec, path); err != nil {
			log.Printf("WARN: render ingress %s/%s: %v", ns, name, err)
			failed = append(failed, "ingress")
		} else if err := g.Runner.RunOnce(ctx, path); err != nil {
			log.Printf("WARN: %v", err)
			failed = append(failed, "ingress")
		} else {
			ok = true
		}
	} else {
		_ = os.Remove(prefix + "-ingress.pxl")
	}

	if rule.Spec.RecorderOTelEndpoint != "" {
		path := prefix + "-egress.pxl"
		if err := g.Loader.WriteFile(pxl.KindEgress, rule.Spec, path); err != nil {
			log.Printf("WARN: render egress %s/%s: %v", ns, name, err)
			failed = append(failed, "egress")
		} else if err := g.Runner.RunOnce(ctx, path); err != nil {
			log.Printf("WARN: %v", err)
			failed = append(failed, "egress")
		} else {
			ok = true
		}
	} else {
		_ = os.Remove(prefix + "-egress.pxl")
	}

	if rule.Spec.MongoOTelEndpoint != "" {
		path := prefix + "-mongo.pxl"
		if err := g.Loader.WriteFile(pxl.KindMongo, rule.Spec, path); err != nil {
			log.Printf("WARN: render mongo %s/%s: %v", ns, name, err)
			failed = append(failed, "mongo")
		} else if err := g.Runner.RunOnce(ctx, path); err != nil {
			log.Printf("WARN: %v", err)
			failed = append(failed, "mongo")
		} else {
			ok = true
		}
	} else {
		_ = os.Remove(prefix + "-mongo.pxl")
	}

	if rule.Spec.OTelEndpoint == "" && rule.Spec.RecorderOTelEndpoint == "" && rule.Spec.MongoOTelEndpoint == "" {
		g.patchStatus(ctx, rule, "Inactive", "no export endpoints")
		return
	}
	if len(failed) > 0 {
		g.patchStatus(ctx, rule, "Error", "px.export failed ("+strings.Join(failed, "+")+")")
		return
	}
	if ok {
		g.patchStatus(ctx, rule, "Active", "px.export ok")
	}
}

func (g *Gate) patchStatus(ctx context.Context, rule *enginesv1alpha1.PixieStreamRule, phase, msg string) {
	base := rule.DeepCopy()
	rule.Status.Phase = phase
	rule.Status.Message = msg
	if err := g.Client.Status().Patch(ctx, rule, client.MergeFrom(base)); err != nil {
		log.Printf("WARN: status patch %s/%s: %v", rule.Namespace, rule.Name, err)
	}
}

func removeGlob(pattern string) {
	matches, _ := filepath.Glob(pattern)
	for _, m := range matches {
		_ = os.Remove(m)
	}
}
