package core

import (
	"context"
	"fmt"

	"github.com/shadow-diff/igris/internal/config"
	"github.com/shadow-diff/igris/internal/driver"
)

// Run starts all configured listeners and blocks until ctx is cancelled.
func Run(ctx context.Context, cfg config.Config, hub *Hub, factories map[string]func() driver.InputDriver) ([]driver.InputDriver, error) {
	var drivers []driver.InputDriver
	for _, l := range cfg.Listeners {
		factory, ok := factories[l.Driver]
		if !ok {
			return drivers, fmt.Errorf("unknown driver %q for port %d", l.Driver, l.Port)
		}
		d := factory()
		if err := d.Listen(ctx, l.Port, hub); err != nil {
			return drivers, fmt.Errorf("listen port %d (%s): %w", l.Port, l.Driver, err)
		}
		drivers = append(drivers, d)
	}
	<-ctx.Done()
	return drivers, nil
}
