package ingesthealth

import (
	"context"
	"errors"
	"testing"
)

func TestSignalFlipsAfterThresholdAndRecovers(t *testing.T) {
	s := New(3)
	if !s.Healthy() {
		t.Fatal("should start healthy")
	}
	fail := errors.New("disk full")
	s.RecordPersist(fail)
	s.RecordPersist(fail)
	if !s.Healthy() {
		t.Fatal("below threshold should stay healthy")
	}
	s.RecordPersist(fail) // 3rd consecutive => flip
	if s.Healthy() {
		t.Fatal("threshold reached should be unhealthy")
	}
	s.RecordPersist(nil) // first success recovers immediately
	if !s.Healthy() {
		t.Fatal("success should recover")
	}
}

func TestSignalIgnoresContextErrors(t *testing.T) {
	s := New(1)
	s.RecordPersist(context.Canceled)
	s.RecordPersist(context.DeadlineExceeded)
	if !s.Healthy() {
		t.Fatal("context cancellation/timeout must not mark storage unhealthy")
	}
}

func TestNilSignalIsHealthy(t *testing.T) {
	var s *Signal
	s.RecordPersist(errors.New("x")) // must not panic
	if !s.Healthy() {
		t.Fatal("nil signal must report healthy (feature disabled)")
	}
}
