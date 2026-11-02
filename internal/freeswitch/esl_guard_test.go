package freeswitch

import (
	"context"
	"errors"
	"testing"
)

// A newline in any argument would end the ESL command and start a new one on
// the same authenticated connection, so the client must refuse before dialing.
// The address is unreachable: reaching the dial would be a different error.
func TestCommandsRejectControlCharacters(t *testing.T) {
	c := New("127.0.0.1:1", "x")
	ctx := context.Background()

	if _, err := c.Command(ctx, "uuid_setvar", "leg transfer_target x\n\napi status"); !errors.Is(err, ErrUnsafeArg) {
		t.Errorf("Command err = %v, want ErrUnsafeArg", err)
	}
	if _, err := c.Command(ctx, "uuid_displace", "leg start x\x00y"); !errors.Is(err, ErrUnsafeArg) {
		t.Errorf("Command NUL err = %v, want ErrUnsafeArg", err)
	}

	// Clean arguments get past the guard (and fail on the dial instead).
	if _, err := c.Command(ctx, "uuid_hold", "off 7a87"); errors.Is(err, ErrUnsafeArg) {
		t.Errorf("clean Command rejected: %v", err)
	}
}
