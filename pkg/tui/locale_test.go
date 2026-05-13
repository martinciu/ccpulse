package tui

import "testing"

func TestExtractRegion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"POSIX form en_US", "en_US", "US"},
		{"BCP-47 form en-US", "en-US", "US"},
		{"BCP-47 with script subtag", "zh-Hans-CN", "CN"},
		{"polish posix", "pl_PL", "PL"},
		{"bare language no region", "en", ""},
		{"empty", "", ""},
		{"lowercase region rejected", "en_us", ""},
		{"three-letter region rejected", "en_USA", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractRegion(tt.in)
			if got != tt.want {
				t.Errorf("extractRegion(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseLocaleEnv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want dateOrder
	}{
		{"en_US.UTF-8 with encoding", "en_US.UTF-8", dateOrderMonthFirst},
		{"en_US bare", "en_US", dateOrderMonthFirst},
		{"en-US BCP-47", "en-US", dateOrderMonthFirst},
		{"en_GB.UTF-8", "en_GB.UTF-8", dateOrderDayFirst},
		{"pl_PL.UTF-8", "pl_PL.UTF-8", dateOrderDayFirst},
		{"de_DE@euro modifier", "de_DE@euro", dateOrderDayFirst},
		{"sr_RS.UTF-8@latin encoding+modifier", "sr_RS.UTF-8@latin", dateOrderDayFirst},
		{"ja_JP per just-US policy", "ja_JP.UTF-8", dateOrderDayFirst},
		{"zh-Hans-CN BCP-47", "zh-Hans-CN", dateOrderDayFirst},
		{"C POSIX fallback", "C", dateOrderMonthFirst},
		{"POSIX explicit", "POSIX", dateOrderMonthFirst},
		{"empty string", "", dateOrderMonthFirst},
		{"bare language no region", "en", dateOrderMonthFirst},
		{"garbage", "garbage", dateOrderMonthFirst},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseLocaleEnv(tt.in)
			if got != tt.want {
				t.Errorf("parseLocaleEnv(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestDetectDateOrder(t *testing.T) {
	// t.Setenv is incompatible with t.Parallel — sequential by design.
	tests := []struct {
		name    string
		lcTime  string
		lcAll   string
		lang    string
		want    dateOrder
		comment string
	}{
		{
			name:    "LC_TIME wins over LC_ALL and LANG",
			lcTime:  "en_GB.UTF-8",
			lcAll:   "en_US.UTF-8",
			lang:    "en_US.UTF-8",
			want:    dateOrderDayFirst,
			comment: "LC_TIME first in chain",
		},
		{
			name:    "LC_ALL wins when LC_TIME empty",
			lcTime:  "",
			lcAll:   "en_GB.UTF-8",
			lang:    "en_US.UTF-8",
			want:    dateOrderDayFirst,
			comment: "LC_ALL second in chain",
		},
		{
			name:    "LANG wins when LC_TIME and LC_ALL empty",
			lcTime:  "",
			lcAll:   "",
			lang:    "pl_PL.UTF-8",
			want:    dateOrderDayFirst,
			comment: "LANG last in chain",
		},
		{
			name:    "all empty falls back to MonthFirst",
			lcTime:  "",
			lcAll:   "",
			lang:    "",
			want:    dateOrderMonthFirst,
			comment: "fail-closed to legacy en_US",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("LC_TIME", tt.lcTime)
			t.Setenv("LC_ALL", tt.lcAll)
			t.Setenv("LANG", tt.lang)
			got := detectDateOrder()
			if got != tt.want {
				t.Errorf("detectDateOrder() = %v, want %v (%s)", got, tt.want, tt.comment)
			}
		})
	}
}
