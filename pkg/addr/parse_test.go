package addr

import (
	"testing"

	"github.com/pdat-cz/pc/pkg/proto"
)

func TestParseReadSpec(t *testing.T) {
	cases := []struct {
		input   string
		want    proto.ReadSpec
		wantErr bool
	}{
		{
			"holding/10",
			proto.ReadSpec{Kind: proto.Holding, Addr: 10},
			false,
		},
		{
			"holding/10@u16",
			proto.ReadSpec{Kind: proto.Holding, Addr: 10, Format: "u16"},
			false,
		},
		{
			"holding/10@f32be",
			proto.ReadSpec{Kind: proto.Holding, Addr: 10, Format: "f32be"},
			false,
		},
		{
			"holding/10:4",
			proto.ReadSpec{Kind: proto.Holding, Addr: 10, Count: 4},
			false,
		},
		{
			"holding/10:4@u16",
			proto.ReadSpec{Kind: proto.Holding, Addr: 10, Count: 4, Format: "u16"},
			false,
		},
		{
			"coil/0",
			proto.ReadSpec{Kind: proto.Coil, Addr: 0},
			false,
		},
		{
			"discrete/5",
			proto.ReadSpec{Kind: proto.Discrete, Addr: 5},
			false,
		},
		{
			"input/3",
			proto.ReadSpec{Kind: proto.Input, Addr: 3},
			false,
		},
		{
			"mbus/7@ud2",
			proto.ReadSpec{Kind: proto.MBus, Addr: 7, Format: "ud2"},
			false,
		},
		{
			"mbus/7",
			proto.ReadSpec{Kind: proto.MBus, Addr: 7},
			false,
		},
		// dollar sign as separator (alternative syntax)
		{
			"holding/10$u16",
			proto.ReadSpec{Kind: proto.Holding, Addr: 10, Format: "u16"},
			false,
		},
		// error cases
		{"invalid", proto.ReadSpec{}, true},
		{"unknown/10", proto.ReadSpec{}, true},
		{"holding/abc", proto.ReadSpec{}, true},
		{"holding/10:abc", proto.ReadSpec{}, true},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseReadSpec(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ParseReadSpec(%q): expected error, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseReadSpec(%q): unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseReadSpec(%q): got %+v, want %+v", tc.input, got, tc.want)
			}
		})
	}
}
