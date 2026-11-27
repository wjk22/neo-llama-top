package main

import "testing"

func TestParseGPULine(t *testing.T) {
	// A laptop GPU: the driver reports neither a power limit nor a fan.
	g, ok := parseGPULine("0, NVIDIA GeForce RTX 3080 Ti Laptop GPU, 550.163.01, 56, 14683, 16384, 54, 87.31, [N/A], [N/A]")
	if !ok {
		t.Fatal("line not parsed")
	}
	want := GPU{Index: 0, Name: "NVIDIA GeForce RTX 3080 Ti Laptop GPU", Driver: "550.163.01",
		Util: 56, MemUsed: 14683, MemTotal: 16384, Temp: 54, Power: 87.31, PowerCap: -1, Fan: -1}
	if g != want {
		t.Errorf("got  %+v\nwant %+v", g, want)
	}
	if gpuName(g) != "RTX 3080 Ti Laptop GPU" {
		t.Errorf("short name = %q", gpuName(g))
	}
	for _, bad := range []string{"", "NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver.", "x, a, b, 1, 2, 3, 4, 5, 6, 7"} {
		if _, ok := parseGPULine(bad); ok {
			t.Errorf("parsed %q", bad)
		}
	}
}
