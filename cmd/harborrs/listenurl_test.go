package main

import "testing"

func TestListenURL(t *testing.T) {
	cases := map[string]string{
		":8088":          "http://localhost:8088/",
		"0.0.0.0:8088":   "http://localhost:8088/",
		"[::]:8088":      "http://localhost:8088/",
		"127.0.0.1:8088": "http://127.0.0.1:8088/",
		"example.com:80": "http://example.com:80/",
		"[::1]:8088":     "http://::1:8088/",
		"":               "",
		"no-colon":       "no-colon",
		":":              ":",
	}
	for in, want := range cases {
		if got := listenURL(in); got != want {
			t.Errorf("listenURL(%q) = %q, want %q", in, got, want)
		}
	}
}
