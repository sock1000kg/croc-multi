package cli

import "testing"

func TestResolveMultiReceiveCode(t *testing.T) {
	tests := []struct {
		name        string
		goos        string
		arg, env    string
		classic     bool
		wantCode    string
		wantRefused bool
	}{
		{"env wins", "linux", "", "env-code", false, "env-code", false},
		{"env wins over arg", "linux", "arg-code", "env-code", false, "env-code", false},
		{"arg refused on unix", "linux", "arg-code", "", false, "", true},
		{"arg refused on mac", "darwin", "arg-code", "", false, "", true},
		{"arg allowed in classic", "linux", "arg-code", "", true, "arg-code", false},
		{"arg allowed on windows", "windows", "arg-code", "", false, "arg-code", false},
		{"nothing: prompt", "linux", "", "", false, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, refused := resolveMultiReceiveCode(tt.goos, tt.arg, tt.env, tt.classic)
			if code != tt.wantCode || refused != tt.wantRefused {
				t.Fatalf("got (%q, %v), want (%q, %v)", code, refused, tt.wantCode, tt.wantRefused)
			}
		})
	}
}
