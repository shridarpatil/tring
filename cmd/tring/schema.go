package main

import (
	"errors"
	"log/slog"
	"time"

	"github.com/shridarpatil/tring/internal/migrate"
)

// waitForSchema blocks a gateway-only node until the database has every
// migration it embeds. Behind means the main node is still migrating (or not
// upgraded yet), so wait; dirty or unreachable needs an operator, so fail.
func waitForSchema(stop <-chan struct{}, ready func() error, every time.Duration, log *slog.Logger) error {
	for {
		err := ready()
		if err == nil {
			return nil
		}
		if !errors.Is(err, migrate.ErrSchemaBehind) {
			return err
		}
		log.Info("waiting for the main node to migrate", "err", err)
		select {
		case <-stop:
			return errors.New("stopped while waiting for the schema")
		case <-time.After(every):
		}
	}
}
