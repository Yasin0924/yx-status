package main

import "testing"

func TestDefaultServerConfigRequiresPrivateNodes(t *testing.T) {
	if nodes := defaultServerConfig().Nodes; len(nodes) != 0 {
		t.Fatal("default server config must not embed node identities or shared secrets")
	}
}
