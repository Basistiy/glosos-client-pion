package config

import (
	"os"
	"testing"
)

func TestGetEnvDefault(t *testing.T) {
	key := "TEST_GLOSOS_CONFIG_VAR"
	_ = os.Unsetenv(key)

	if val := getEnvDefault(key, "default_val"); val != "default_val" {
		t.Errorf("expected 'default_val', got %q", val)
	}

	_ = os.Setenv(key, "custom_val")
	defer os.Unsetenv(key)

	if val := getEnvDefault(key, "default_val"); val != "custom_val" {
		t.Errorf("expected 'custom_val', got %q", val)
	}
}

func TestGetEnvBoolDefault(t *testing.T) {
	key := "TEST_GLOSOS_BOOL_VAR"
	_ = os.Unsetenv(key)

	if val := getEnvBoolDefault(key, true); val != true {
		t.Errorf("expected true, got %v", val)
	}

	_ = os.Setenv(key, "false")
	defer os.Unsetenv(key)

	if val := getEnvBoolDefault(key, true); val != false {
		t.Errorf("expected false, got %v", val)
	}

	_ = os.Setenv(key, "1")
	if val := getEnvBoolDefault(key, false); val != true {
		t.Errorf("expected true, got %v", val)
	}
}
