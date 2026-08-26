package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
)

// TestSignOutKeepsTheOtherDeviceSignedIn opens two independent sessions for the
// same student and checks that ending one of them leaves the other usable.
func TestSignOutKeepsTheOtherDeviceSignedIn(t *testing.T) {
	crew := newHarness(t)
	ctx := context.Background()

	phone, err := crew.auth.Login(ctx, "student@campus.example", testPassword, "campus-phone")
	if err != nil {
		t.Fatalf("the first sign in failed: %v", err)
	}
	laptop, err := crew.auth.Login(ctx, "student@campus.example", testPassword, "lab-laptop")
	if err != nil {
		t.Fatalf("the second sign in failed: %v", err)
	}
	if phone.Token == laptop.Token || phone.Principal.SessionID == laptop.Principal.SessionID {
		t.Fatalf("the two sign ins must be independent, phone session %d laptop session %d",
			phone.Principal.SessionID, laptop.Principal.SessionID)
	}

	opened, err := crew.auth.ActiveSessionCount(ctx, laptop.Principal)
	if err != nil {
		t.Fatalf("counting the open sessions failed: %v", err)
	}
	if opened != 2 {
		t.Fatalf("both sign ins must be open before the sign out, counted %d", opened)
	}

	if err := crew.auth.Logout(ctx, phone.Principal); err != nil {
		t.Fatalf("signing out the phone failed: %v", err)
	}

	if _, err := crew.auth.Authenticate(ctx, phone.Token); !errors.Is(err, domain.ErrSessionRevoked) {
		t.Fatalf("the phone credential must be closed after its own sign out, got %v", err)
	}

	survivor, err := crew.auth.Authenticate(ctx, laptop.Token)
	if err != nil {
		t.Fatalf("the laptop credential must survive the phone sign out, got %v (code %q)",
			err, domain.Code(err))
	}
	if survivor.SessionID != laptop.Principal.SessionID {
		t.Fatalf("the laptop resolved to session %d instead of %d",
			survivor.SessionID, laptop.Principal.SessionID)
	}

	remaining, err := crew.auth.ActiveSessionCount(ctx, survivor)
	if err != nil {
		t.Fatalf("counting the remaining sessions failed: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("exactly the laptop session must remain open, counted %d", remaining)
	}
}
