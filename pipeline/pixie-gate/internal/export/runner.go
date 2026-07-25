package export

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

var uuidRE = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// Runner shells out to the Pixie CLI (`px run`).
type Runner struct {
	PxPath  string        // default "px"
	Timeout time.Duration // default 25s
	mu      sync.Mutex
	cluster string // cached CS_HEALTHY vizier UUID
}

func (r *Runner) px() string {
	if r.PxPath == "" {
		return "px"
	}
	return r.PxPath
}

func (r *Runner) timeout() time.Duration {
	if r.Timeout <= 0 {
		return 25 * time.Second
	}
	return r.Timeout
}

// EnsureAuth runs `px auth login --use_api_key` when PIXIE_API_KEY (or PX_API_KEY) is set.
// Persists a refresh token under $HOME/.pixie for subsequent px run calls.
func (r *Runner) EnsureAuth(ctx context.Context) error {
	key := os.Getenv("PIXIE_API_KEY")
	if key == "" {
		key = os.Getenv("PX_API_KEY")
	}
	if key == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, r.px(), "auth", "login", "--use_api_key", "--api_key="+key)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("px auth login: %w (%s)", err, truncate(string(out), 200))
	}
	return nil
}

// ResolveClusterID returns the first CS_HEALTHY vizier UUID (cached).
func (r *Runner) ResolveClusterID(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cluster != "" {
		return r.cluster, nil
	}
	cmd := exec.CommandContext(ctx, r.px(), "get", "viziers")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("px get viziers: %w (%s)", err, truncate(string(out), 200))
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "CS_HEALTHY") {
			continue
		}
		if id := uuidRE.FindString(line); id != "" {
			r.cluster = id
			return id, nil
		}
	}
	return "", fmt.Errorf("no CS_HEALTHY vizier in px get viziers output")
}

// RunOnce executes `px run -c <id> -f <pxl>` with a hard timeout.
// ponytail: px run blocks on OTLP export; timeout keeps the gate killable via SIGTERM.
func (r *Runner) RunOnce(ctx context.Context, pxlFile string) error {
	clusterID, err := r.ResolveClusterID(ctx)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()
	args := []string{"run", "-c", clusterID, "-f", pxlFile}
	cmd := exec.CommandContext(runCtx, r.px(), args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("px run %s: %w (%s)", pxlFile, err, truncate(buf.String(), 200))
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
