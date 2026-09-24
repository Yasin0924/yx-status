package main

import "testing"

func TestDefaultAgentConfigHasNoIdentitySecret(t *testing.T) {
	cfg := defaultAgentConfig()
	if cfg.NodeID != "" || cfg.Secret != "" {
		t.Fatal("default agent config must require a private node identity and secret")
	}
}
