package main

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLANAddressValidation(t *testing.T) {
	for _, test := range []struct {
		address string
		valid   bool
	}{
		{"192.168.70.1/24", true},
		{"192.168.70.0/24", false},
		{"192.168.70.255/24", false},
		{"10.0.0.1/8", true},
	} {
		if got := validHostAddress(netip.MustParsePrefix(test.address)); got != test.valid {
			t.Errorf("%s: got %v, want %v", test.address, got, test.valid)
		}
	}
}

func TestLANFileMustMatchRunningAddress(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pilot-eth1")
	content := []byte("auto eth1\niface eth1 inet static\n    address 192.168.60.1/24\n")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	m := &networkManager{interfacesDir: dir}
	if _, _, err := m.readLANFile("eth1", netip.MustParsePrefix("192.168.60.1/24")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.readLANFile("eth1", netip.MustParsePrefix("192.168.61.1/24")); err == nil {
		t.Fatal("mismatched live address was accepted")
	}
	replacement := strings.Replace(string(content), "192.168.60.1/24", "192.168.70.1/24", 1)
	if err := writeAtomic(path, []byte(replacement), 0644); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != replacement {
		t.Fatalf("updated config: %q, %v", actual, err)
	}
}
