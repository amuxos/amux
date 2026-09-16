package extensionstate

import (
	"errors"
	"runtime"
	"testing"
	"time"
)

func TestFreezeWaitsForRegistrationAndRejectsNestedEntry(t *testing.T) {
	var registration gate
	release, err := registration.beginRegistration()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	nestedRelease, err := registration.beginRegistration()
	if err != nil {
		t.Fatalf("nested startup registration: %v", err)
	}
	nestedRelease()

	frozen := make(chan struct{})
	go func() { registration.freeze(); close(frozen) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		nestedRelease, err := registration.beginRegistration()
		if errors.Is(err, ErrFrozen) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		nestedRelease()
		if time.Now().After(deadline) {
			t.Fatal("Freeze did not close new admission")
		}
		runtime.Gosched()
	}
	select {
	case <-frozen:
		t.Fatal("Freeze returned while an admitted registration was active")
	default:
	}
	release()
	release = nil
	select {
	case <-frozen:
	case <-time.After(2 * time.Second):
		t.Fatal("Freeze deadlocked with nested registration")
	}
	registration.freeze()
	if release, err := registration.beginRegistration(); release != nil || !errors.Is(err, ErrFrozen) {
		t.Fatalf("registration reopened after Freeze: %v", err)
	}
}
