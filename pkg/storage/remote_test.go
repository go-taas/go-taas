package storage

import "testing"

// TestJoinObjectKey covers the weight paths the model service stores: some
// carry a trailing slash, some do not, and a relative path never starts with
// one. A doubled separator becomes part of the object name and object stores
// reject it.
func TestJoinObjectKey(t *testing.T) {
	cases := []struct {
		prefix string
		rel    string
		want   string
	}{
		{"model/version/", "config.json", "model/version/config.json"},
		{"model/version", "config.json", "model/version/config.json"},
		{"/model/version/", "/nested/config.json", "model/version/nested/config.json"},
		{"", "config.json", "config.json"},
		{"model/", "", "model"},
	}
	for _, tc := range cases {
		if got := joinObjectKey(tc.prefix, tc.rel); got != tc.want {
			t.Errorf("joinObjectKey(%q, %q) = %q, want %q", tc.prefix, tc.rel, got, tc.want)
		}
	}
}
