package hostdir

import "testing"

func TestSnapshotIsSIWC(t *testing.T) {
	cases := []struct {
		name     string
		metadata string
		want     bool
	}{
		{"siwc provider", `{"id":1,"extra":{"auth_provider":"siwc"}}`, true},
		{"codex provider", `{"id":2,"extra":{"auth_provider":"codex"}}`, false},
		{"no extra", `{"id":3}`, false},
		{"empty", ``, false},
		{"invalid json", `{`, false},
		{"credentials never visible", `{"id":4,"credentials":{"auth_mode":"siwc"}}`, false},
	}
	for _, testCase := range cases {
		if got := snapshotIsSIWC([]byte(testCase.metadata)); got != testCase.want {
			t.Errorf("%s: got %v want %v", testCase.name, got, testCase.want)
		}
	}
}
