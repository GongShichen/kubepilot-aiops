package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFaultControllerAuthenticationAndReset(t *testing.T) {
	controller := newFaultController("", "control-token")
	unauthorized := httptest.NewRequest(http.MethodPost, "/benchmark/v1/fault", strings.NewReader(`{"mode":"busy_loop"}`))
	unauthorizedResult := httptest.NewRecorder()
	controller.handle(unauthorizedResult, unauthorized)
	if unauthorizedResult.Code != http.StatusUnauthorized || controller.modeValue() != "" {
		t.Fatalf("unauthorized request changed mode: status=%d mode=%q", unauthorizedResult.Code, controller.modeValue())
	}

	request := httptest.NewRequest(http.MethodPost, "/benchmark/v1/fault", strings.NewReader(`{"mode":"busy_loop"}`))
	request.Header.Set("X-KubePilot-Benchmark-Token", "control-token")
	result := httptest.NewRecorder()
	controller.handle(result, request)
	if result.Code != http.StatusNoContent || controller.modeValue() != "busy_loop" {
		t.Fatalf("status=%d mode=%q", result.Code, controller.modeValue())
	}

	reset := httptest.NewRequest(http.MethodDelete, "/benchmark/v1/fault", nil)
	reset.Header.Set("X-KubePilot-Benchmark-Token", "control-token")
	resetResult := httptest.NewRecorder()
	controller.handle(resetResult, reset)
	if resetResult.Code != http.StatusNoContent || controller.modeValue() != "" {
		t.Fatalf("reset status=%d mode=%q", resetResult.Code, controller.modeValue())
	}
}
