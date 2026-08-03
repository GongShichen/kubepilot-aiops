package graph

import (
	"strings"
	"testing"
)

func TestRedactErrorRemovesEndpointAndCredentials(t *testing.T) {
	raw := "POST https://models.example.test/v1/messages failed: Authorization: Bearer-secret x-api-key=top-secret"
	redacted := RedactError(raw)
	for _, secret := range []string{"models.example.test", "Bearer-secret", "top-secret"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("sensitive value %q remains in %q", secret, redacted)
		}
	}
}
