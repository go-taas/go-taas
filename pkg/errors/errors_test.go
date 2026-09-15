package errors

import (
	"errors"
	"testing"
)

func TestNewAndError(t *testing.T) {
	err := New(CodeModelNotFound)
	if err.Code != CodeModelNotFound {
		t.Fatalf("unexpected code: %d", err.Code)
	}
	if err.Error() != "[10101] model not found" {
		t.Fatalf("unexpected message: %s", err.Error())
	}
}

func TestWrapAndUnwrap(t *testing.T) {
	cause := errors.New("disk full")
	err := Wrap(CodeImageWarmupFailed, cause, "warmup node %s", "node-1")
	if err.Message != "warmup node node-1" {
		t.Fatalf("unexpected message: %s", err.Message)
	}
	if !errors.Is(err, cause) {
		t.Fatal("wrapped cause not reachable via errors.Is")
	}
	if got := CodeOf(err); got != CodeImageWarmupFailed {
		t.Fatalf("unexpected code: %d", got)
	}
}

func TestCodeOfDefaults(t *testing.T) {
	if got := CodeOf(nil); got != CodeOK {
		t.Fatalf("nil should map to CodeOK, got %d", got)
	}
	if got := CodeOf(errors.New("boom")); got != CodeInternal {
		t.Fatalf("plain error should map to CodeInternal, got %d", got)
	}
}

func TestMessageOf(t *testing.T) {
	if got := MessageOf(nil); got != "OK" {
		t.Fatalf("nil should map to OK, got %q", got)
	}
	if got := MessageOf(errors.New("boom")); got != "internal error" {
		t.Fatalf("plain error should map to internal error, got %q", got)
	}
	if got := MessageOf(New(CodeAPIKeyRevoked)); got != "API key revoked" {
		t.Fatalf("unexpected canonical message: %q", got)
	}
}

func TestMessageUnknownCode(t *testing.T) {
	if got := Message(Code(99999)); got != "internal error" {
		t.Fatalf("unknown code should fall back, got %q", got)
	}
}

func TestAs(t *testing.T) {
	err := Newf(CodeInsufficientFunds, "balance %.2f", 1.5)
	ae, ok := As(err)
	if !ok {
		t.Fatal("As should extract APIError")
	}
	if ae.Message != "balance 1.50" {
		t.Fatalf("unexpected message: %s", ae.Message)
	}
	if _, ok := As(errors.New("plain")); ok {
		t.Fatal("As should not extract from plain error")
	}
}
