package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/shridarpatil/tring/internal/migrate"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestWaitForSchemaRetriesWhileBehind(t *testing.T) {
	calls := 0
	ready := func() error {
		if calls++; calls < 3 {
			return fmt.Errorf("%w: database at 28, binary needs 29", migrate.ErrSchemaBehind)
		}
		return nil
	}
	if err := waitForSchema(nil, ready, time.Millisecond, quiet); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Errorf("ready called %d times, want 3", calls)
	}
}

func TestWaitForSchemaDirtyFails(t *testing.T) {
	ready := func() error { return migrate.ErrSchemaDirty }
	if err := waitForSchema(nil, ready, time.Millisecond, quiet); !errors.Is(err, migrate.ErrSchemaDirty) {
		t.Fatalf("err = %v, want ErrSchemaDirty", err)
	}
}

func TestWaitForSchemaStops(t *testing.T) {
	ready := func() error { return migrate.ErrSchemaBehind }
	if err := waitForSchema(stopped(), ready, time.Hour, quiet); err == nil {
		t.Fatal("want an error when stopped while waiting")
	}
}
