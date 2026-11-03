package auction

import "testing"

func TestParseCPMMicros(t *testing.T) {
	tests := []struct {
		input string
		want  int64
		ok    bool
	}{
		{"2.5", 2_500_000, true},
		{"0.000001", 1, true},
		{"1.200000", 1_200_000, true},
		{"0.0000004", 0, true},
		{"0.0000005", 0, true},
		{"0.0000015", 2, true},
		{"0.0000025", 2, true},
		{"12", 12_000_000, true},
		{"-1", 0, false},
		{"1e3", 0, false},
		{"1.2.3", 0, false},
		{"", 0, false},
	}
	for _, test := range tests {
		got, err := ParseCPMMicros(test.input)
		if test.ok && err != nil {
			t.Fatalf("ParseCPMMicros(%q) unexpected error: %v", test.input, err)
		}
		if !test.ok && err == nil {
			t.Fatalf("ParseCPMMicros(%q) expected error", test.input)
		}
		if got != test.want {
			t.Fatalf("ParseCPMMicros(%q) = %d, want %d", test.input, got, test.want)
		}
	}
}

func TestFormatCPMMicros(t *testing.T) {
	if got := FormatCPMMicros(2_500_000); got != "2.500000" {
		t.Fatalf("unexpected formatting: %s", got)
	}
	if got := FormatCPMMicros(1); got != "0.000001" {
		t.Fatalf("unexpected formatting: %s", got)
	}
}
