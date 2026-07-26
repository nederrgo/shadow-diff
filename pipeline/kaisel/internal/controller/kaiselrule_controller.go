package controller

import (
	"context"
	"net"
	"sync"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/shadow-diff/kaisel/internal/capture"
	"github.com/shadow-diff/kaisel/internal/export"
	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

type ruleState struct {
	ips              map[string]bool
	ports            map[uint16]bool
	igrisBaseURL     string
	egressBaseURL    string
	samplePercentage int
}

// Reconciler watches KaiselRule CRs and sends MapUpdates into the capture loop.
type Reconciler struct {
	client  client.Client
	updates chan<- capture.MapUpdate
	router  *export.Router
	mu      sync.Mutex
	byRule  map[string]ruleState
}

func New(c client.Client, updates chan<- capture.MapUpdate, router *export.Router) *Reconciler {
	return &Reconciler{
		client:  c,
		updates: updates,
		router:  router,
		byRule:  make(map[string]ruleState),
	}
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var rule enginev1alpha1.KaiselRule
	err := r.client.Get(ctx, req.NamespacedName, &rule)
	deleted := client.IgnoreNotFound(err) == nil && err != nil

	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	prev := r.byRule[req.String()]
	var next ruleState
	if !deleted && rule.DeletionTimestamp == nil {
		next = stateFrom(rule.Spec)
	}

	upd := diff(prev, next)
	if deleted || (err == nil && rule.DeletionTimestamp != nil) {
		delete(r.byRule, req.String())
	} else if err == nil {
		r.byRule[req.String()] = next
	}

	r.rebuildRouter()

	if hasChanges(upd) {
		// ponytail: non-blocking; next reconcile resyncs if the channel is full
		select {
		case r.updates <- upd:
		default:
		}
	}

	return ctrl.Result{}, nil
}

func (r *Reconciler) rebuildRouter() {
	if r.router == nil {
		return
	}
	rules := make([]export.RuleExport, 0, len(r.byRule))
	for key, st := range r.byRule {
		ips := make([]string, 0, len(st.ips))
		for ip := range st.ips {
			ips = append(ips, ip)
		}
		rules = append(rules, export.RuleExport{
			Key:              key,
			IPs:              ips,
			IgrisBaseURL:     st.igrisBaseURL,
			EgressBaseURL:    st.egressBaseURL,
			SamplePercentage: st.samplePercentage,
		})
	}
	r.router.Rebuild(rules)
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&enginev1alpha1.KaiselRule{}).
		Complete(r)
}

func stateFrom(spec enginev1alpha1.KaiselRuleSpec) ruleState {
	s := ruleState{
		ips:              make(map[string]bool, len(spec.TargetIPs)),
		ports:            make(map[uint16]bool, len(spec.TargetPorts)),
		igrisBaseURL:     spec.IgrisBaseURL,
		egressBaseURL:    spec.EgressBaseURL,
		samplePercentage: spec.SamplePercentage,
	}
	for _, ip := range spec.TargetIPs {
		s.ips[ip] = true
	}
	for _, p := range spec.TargetPorts {
		s.ports[p] = true
	}
	return s
}

func diff(prev, next ruleState) capture.MapUpdate {
	var upd capture.MapUpdate
	for ip := range next.ips {
		if !prev.ips[ip] {
			upd.AddIPs = append(upd.AddIPs, net.ParseIP(ip))
		}
	}
	for ip := range prev.ips {
		if !next.ips[ip] {
			upd.RemoveIPs = append(upd.RemoveIPs, net.ParseIP(ip))
		}
	}
	for p := range next.ports {
		if !prev.ports[p] {
			upd.AddPorts = append(upd.AddPorts, p)
		}
	}
	for p := range prev.ports {
		if !next.ports[p] {
			upd.RemovePorts = append(upd.RemovePorts, p)
		}
	}
	return upd
}

func hasChanges(upd capture.MapUpdate) bool {
	return len(upd.AddIPs) > 0 || len(upd.RemoveIPs) > 0 ||
		len(upd.AddPorts) > 0 || len(upd.RemovePorts) > 0
}
