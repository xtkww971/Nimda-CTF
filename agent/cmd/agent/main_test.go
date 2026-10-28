package main

import "testing"

func TestParsePortRange(t *testing.T) {
	ok := []struct {
		in         string
		lo, hi     int
	}{
		{"30000-30199", 30000, 30199},
		{" 30000 - 30199 ", 30000, 30199}, // 공백 허용
		{"40000-40000", 40000, 40000},     // 한 개짜리 범위
	}
	for _, tc := range ok {
		lo, hi, err := parsePortRange(tc.in)
		if err != nil || lo != tc.lo || hi != tc.hi {
			t.Errorf("parsePortRange(%q) = %d,%d,%v; want %d,%d,nil", tc.in, lo, hi, err, tc.lo, tc.hi)
		}
	}

	bad := []string{"", "30000", "abc-30199", "30000-xyz", "30200-30000", "0-10", "30000-70000"}
	for _, in := range bad {
		if _, _, err := parsePortRange(in); err == nil {
			t.Errorf("parsePortRange(%q)이 에러를 내지 않음", in)
		}
	}
}
