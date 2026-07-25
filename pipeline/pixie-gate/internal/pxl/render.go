package pxl

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	enginesv1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

//go:embed templates/*.tmpl
var embeddedTemplates embed.FS

// Kind selects which PxL template to render.
type Kind string

const (
	KindIngress Kind = "ingress"
	KindEgress  Kind = "egress"
	KindMongo   Kind = "mongo"
)

func templateFile(kind Kind) string {
	switch kind {
	case KindIngress:
		return "http-ingress-export.pxl.tmpl"
	case KindEgress:
		return "http-egress-export.pxl.tmpl"
	case KindMongo:
		return "mongodb-export.pxl.tmpl"
	default:
		return ""
	}
}

// Loader resolves PxL templates from a directory (ConfigMap mount) or embedded defaults.
type Loader struct {
	Dir string // optional; if set and file exists, preferred over embed
}

func (l Loader) load(kind Kind) (string, error) {
	name := templateFile(kind)
	if name == "" {
		return "", fmt.Errorf("unknown PxL kind %q", kind)
	}
	if l.Dir != "" {
		path := filepath.Join(l.Dir, name)
		if b, err := os.ReadFile(path); err == nil {
			return string(b), nil
		}
	}
	b, err := embeddedTemplates.ReadFile("templates/" + name)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Render fills a template for the given rule kind.
func (l Loader) Render(kind Kind, spec enginesv1alpha1.PixieStreamRuleSpec) (string, error) {
	tpl, err := l.load(kind)
	if err != nil {
		return "", err
	}
	if kind == KindMongo {
		return renderMongo(tpl, spec), nil
	}
	return renderHTTP(tpl, kind, spec), nil
}

func renderMongo(tpl string, spec enginesv1alpha1.PixieStreamRuleSpec) string {
	text := strings.ReplaceAll(tpl, "__SHADOW_NAMESPACE__", spec.ShadowNamespace)
	return strings.ReplaceAll(text, "__MONGO_OTEL_ENDPOINT__", spec.MongoOTelEndpoint)
}

func renderHTTP(tpl string, kind Kind, spec enginesv1alpha1.PixieStreamRuleSpec) string {
	pct := SamplePct(spec.SamplePercentage)
	exclude := strings.TrimSpace(ExcludePathLines(spec.ExcludePaths))
	labels := strings.TrimSpace(LabelFilterLines(spec.TargetLabels))

	var remote, ports, hosts, sample string
	if kind == KindIngress {
		remote = "# no remote client filters"
		ports = strings.TrimSpace(PortFilterLines(spec.TargetPorts))
		hosts = "# ingress: prod pod label filters"
		sample = strings.TrimSpace(SampleFilterLines(pct, "trace_hdr"))
	} else {
		remote = strings.TrimSpace(RemoteClientFilterLines(spec.TargetLabels))
		ports = "# egress: no local_port filter"
		hosts = "# egress: dual-branch client+server"
		sample = strings.TrimSpace(SampleFilterLines(pct, "traceparent"))
	}

	ns := spec.TargetNamespace
	if ns == "" {
		ns = "default"
	}
	text := tpl
	text = strings.ReplaceAll(text, "__TARGET_NAMESPACE__", ns)
	text = strings.ReplaceAll(text, "__OTEL_ENDPOINT__", spec.OTelEndpoint)
	text = strings.ReplaceAll(text, "__RECORDER_OTEL_ENDPOINT__", spec.RecorderOTelEndpoint)
	text = strings.ReplaceAll(text, "__LABEL_FILTERS__", labels)
	text = strings.ReplaceAll(text, "__REMOTE_CLIENT_FILTERS__", remote)
	text = strings.ReplaceAll(text, "__EXCLUDE_PATH_FILTERS__", exclude)
	text = strings.ReplaceAll(text, "__PORT_FILTERS__", ports)
	text = strings.ReplaceAll(text, "__EGRESS_HOST_FILTERS__", hosts)
	text = strings.ReplaceAll(text, "__SAMPLE_FILTERS__", sample)
	return text
}

// WriteFile renders and writes a .pxl script to path.
func (l Loader) WriteFile(kind Kind, spec enginesv1alpha1.PixieStreamRuleSpec, path string) error {
	body, err := l.Render(kind, spec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}
