package mbus

import (
	"math"
	"testing"
)

// testFrame builds a valid M-Bus long frame from payload bytes (automatically computes CS).
func testFrame(payload []byte) []byte {
	L := byte(len(payload))
	var cs byte
	for _, b := range payload {
		cs += b
	}
	frame := make([]byte, 0, 6+len(payload))
	frame = append(frame, 0x68, L, L, 0x68)
	frame = append(frame, payload...)
	frame = append(frame, cs, 0x16)
	return frame
}

// minimalHeader returns a full M-Bus frame payload (C + A + CI=0x72 + variable data header)
// for a heat meter (hot_water, mfr KAM, id 12345678).
// KAM: K=11, A=1, M=13 → word=(11<<10)|(1<<5)|13 = 11309 = 0x2C2D → lo=0x2D, hi=0x2C
func minimalHeader() []byte {
	return []byte{
		0x08,       // C: RSP_UD (response control byte)
		0x01,       // A: meter address
		0x72,       // CI: variable data
		0x78, 0x56, 0x34, 0x12, // ID: 12345678 BCD (little-endian)
		0x2D, 0x2C, // Manufacturer: KAM
		0x01,       // Version
		0x06,       // Medium: hot_water
		0x55,       // Access number
		0x00,       // Status
		0x00, 0x00, // Signature
	}
}

func TestDecodeFrame_InvalidFrames(t *testing.T) {
	cases := []struct {
		name    string
		frame   []byte
		wantErr bool
	}{
		{"too short", []byte{0x68, 0x01, 0x01}, true},
		{"bad start byte", []byte{0x00, 0x01, 0x01, 0x68, 0x72, 0x01, 0x16}, true},
		{"length mismatch", []byte{0x68, 0x05, 0x06, 0x68, 0x72, 0x00, 0x16}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeFrame(tc.frame)
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestDecodeFrame_UnsupportedCI(t *testing.T) {
	// CI=0x7A (application reset) → should return (nil, nil)
	// payload: C=0x08, A=0x01, CI=0x7A, one data byte
	payload := []byte{0x08, 0x01, 0x7A, 0x00}
	frame := testFrame(payload)
	df, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if df != nil {
		t.Errorf("expected nil for unsupported CI, got %+v", df)
	}
}

func TestDecodeFrame_Header(t *testing.T) {
	payload := minimalHeader() // just header, no records
	frame := testFrame(payload)
	df, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if df == nil {
		t.Fatal("expected non-nil DecodedFrame")
	}
	if df.ID != "12345678" {
		t.Errorf("ID: got %q, want %q", df.ID, "12345678")
	}
	if df.Manufacturer != "KAM" {
		t.Errorf("Manufacturer: got %q, want %q", df.Manufacturer, "KAM")
	}
	if df.Medium != "hot_water" {
		t.Errorf("Medium: got %q, want %q", df.Medium, "hot_water")
	}
	if df.Version != 0x01 {
		t.Errorf("Version: got %d, want 1", df.Version)
	}
	if df.AccessNo != 0x55 {
		t.Errorf("AccessNo: got %d, want 0x55", df.AccessNo)
	}
}

func TestDecodeFrame_EnergyRecord(t *testing.T) {
	// DIF=0x04 (32-bit int, instantaneous), VIF=0x04 (energy Wh, mult=10^1=10)
	// data = 10000 LE = 0x10 0x27 0x00 0x00 → 10000 * 10 = 100000 Wh
	payload := append(minimalHeader(),
		0x04, 0x04, 0x10, 0x27, 0x00, 0x00,
	)
	frame := testFrame(payload)
	df, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if len(df.Records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(df.Records))
	}
	r := df.Records[0]
	if r.DataFunction != "instantaneous" {
		t.Errorf("DataFunction: got %q, want %q", r.DataFunction, "instantaneous")
	}
	if r.Quantity != "energy" {
		t.Errorf("Quantity: got %q, want %q", r.Quantity, "energy")
	}
	if r.Unit != "Wh" {
		t.Errorf("Unit: got %q, want %q", r.Unit, "Wh")
	}
	v, ok := r.Value.(float64)
	if !ok {
		t.Fatalf("Value type: got %T, want float64", r.Value)
	}
	if math.Abs(v-100000) > 0.001 {
		t.Errorf("Value: got %g, want 100000", v)
	}
}

func TestDecodeFrame_VolumeAndTemperature(t *testing.T) {
	// VIF=0x14: volume m³, n=4, mult=10^(4-6)=10^-2=0.01
	// data=1000 LE → 1000 * 0.01 = 10.0 m³
	//
	// VIF=0x5A: flow temperature °C, n=2, mult=10^(2-3)=0.1
	// data=400 LE (0x90 0x01) → 400 * 0.1 = 40.0 °C
	payload := append(minimalHeader(),
		0x04, 0x14, 0xE8, 0x03, 0x00, 0x00, // volume
		0x02, 0x5A, 0x90, 0x01, // flow temperature
	)
	frame := testFrame(payload)
	df, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if len(df.Records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(df.Records))
	}

	vol := df.Records[0]
	if vol.Quantity != "volume" || vol.Unit != "m3" {
		t.Errorf("record[0]: got quantity=%q unit=%q", vol.Quantity, vol.Unit)
	}
	if v, ok := vol.Value.(float64); !ok || math.Abs(v-10.0) > 0.001 {
		t.Errorf("volume value: got %v, want 10.0", vol.Value)
	}

	temp := df.Records[1]
	if temp.Quantity != "flow_temperature" || temp.Unit != "°C" {
		t.Errorf("record[1]: got quantity=%q unit=%q", temp.Quantity, temp.Unit)
	}
	if v, ok := temp.Value.(float64); !ok || math.Abs(v-40.0) > 0.001 {
		t.Errorf("flow_temperature value: got %v, want 40.0", temp.Value)
	}
}

func TestDecodeFrame_BCDRecord(t *testing.T) {
	// DIF=0x0C (8-digit BCD, instantaneous), VIF=0x14 (volume m³, mult=0.01)
	// BCD data: 0x78 0x56 0x34 0x12 → BCD LE → 12345678 * 0.01 = 123456.78 m³
	payload := append(minimalHeader(),
		0x0C, 0x14, 0x78, 0x56, 0x34, 0x12,
	)
	frame := testFrame(payload)
	df, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if len(df.Records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(df.Records))
	}
	r := df.Records[0]
	v, ok := r.Value.(float64)
	if !ok {
		t.Fatalf("Value type: got %T, want float64", r.Value)
	}
	if math.Abs(v-123456.78) > 0.001 {
		t.Errorf("BCD volume: got %g, want 123456.78", v)
	}
}

func TestDecodeFrame_IdleFillersSkipped(t *testing.T) {
	// Idle filler bytes (0x2F) before a real record should be skipped
	payload := append(minimalHeader(),
		0x2F, 0x2F, // fillers
		0x01, 0x00, 0x42, // DIF=1byte int, VIF=0x00 (energy Wh, mult=0.001), data=0x42=66
	)
	frame := testFrame(payload)
	df, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if len(df.Records) != 1 {
		t.Fatalf("expected 1 record (fillers skipped), got %d", len(df.Records))
	}
}

func TestDecodeFrame_DateRecord(t *testing.T) {
	// VIF=0x6C (date, Type G), DIF=0x02 (16-bit int)
	// Type G: bits 0-4=day, 5-8=month, 9-15=year
	// 2023-11-15: day=15=0x0F, month=11=0x0B, year=23
	// word = 15 | (11<<5) | (23<<9) = 15 | 352 | 11776 = 12143 = 0x2F6F
	// LE bytes: 0x6F 0x2F
	payload := append(minimalHeader(),
		0x02, 0x6C, 0x6F, 0x2F,
	)
	frame := testFrame(payload)
	df, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if len(df.Records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(df.Records))
	}
	r := df.Records[0]
	if r.Quantity != "time_point" {
		t.Errorf("Quantity: got %q, want %q", r.Quantity, "time_point")
	}
	s, ok := r.Value.(string)
	if !ok {
		t.Fatalf("Value type: got %T, want string", r.Value)
	}
	if s != "2023-11-15" {
		t.Errorf("Date: got %q, want %q", s, "2023-11-15")
	}
}

func TestDecodeBCD(t *testing.T) {
	cases := []struct {
		b    []byte
		want int64
	}{
		{[]byte{0x42}, 42},
		{[]byte{0x99}, 99},
		{[]byte{0x78, 0x56, 0x34, 0x12}, 12345678},
		{[]byte{0x00}, 0},
	}
	for _, tc := range cases {
		got := decodeBCD(tc.b)
		if got != tc.want {
			t.Errorf("decodeBCD(%X): got %d, want %d", tc.b, got, tc.want)
		}
	}
}

func TestDecodeMfr(t *testing.T) {
	// KAM: K(11) A(1) M(13) → word=(11<<10)|(1<<5)|13=11309=0x2C2D
	got := decodeMfr(0x2D, 0x2C)
	if got != "KAM" {
		t.Errorf("decodeMfr: got %q, want %q", got, "KAM")
	}
}

func TestDecodeBCDID(t *testing.T) {
	got := decodeBCDID([]byte{0x78, 0x56, 0x34, 0x12})
	if got != "12345678" {
		t.Errorf("decodeBCDID: got %q, want %q", got, "12345678")
	}
}
