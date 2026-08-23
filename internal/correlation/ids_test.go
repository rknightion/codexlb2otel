package correlation

import (
	"testing"

	"github.com/rknightion/codexlb2otel/internal/turn"
)

func TestResponseKeyUsesServerTurnIDAsFinalFallback(t *testing.T) {
	tt := &turn.Turn{TurnID: "turn-server"}
	if got := ResponseKey(tt); got != "turn-server" {
		t.Fatalf("ResponseKey = %q, want server TurnID fallback", got)
	}
	if !ResponseSpanID(tt).IsValid() {
		t.Fatal("ResponseSpanID is invalid")
	}
}
