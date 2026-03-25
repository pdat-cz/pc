package mbus

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

// DecodedFrame is a fully parsed M-Bus variable data frame (CI=0x72).
type DecodedFrame struct {
	ID           string   `json:"id"`
	Manufacturer string   `json:"manufacturer"`
	Version      uint8    `json:"version"`
	Medium       string   `json:"medium"`
	AccessNo     uint8    `json:"access_no"`
	Status       uint8    `json:"status"`
	Records      []Record `json:"records"`
}

// Record is a single decoded data record from the frame payload.
type Record struct {
	DataFunction string `json:"data_function"`
	Quantity     string `json:"quantity"`
	Unit         string `json:"unit,omitempty"`
	Value        any    `json:"value"`
	VIFRaw       string `json:"vif_raw,omitempty"` // set when quantity is "unknown"
	Error        string `json:"error,omitempty"`   // set when decoding fails
	StorageNo    uint64 `json:"storage_no"`        // 0 = current value
	Tariff       uint   `json:"tariff"`            // 0 = no tariff
}

// DecodeFrame decodes a full M-Bus long frame (0x68 L L 0x68 ... CS 0x16).
// Returns (nil, nil) when the CI field is not supported (not an error to the caller).
func DecodeFrame(frame []byte) (*DecodedFrame, error) {
	if len(frame) < 6 {
		return nil, fmt.Errorf("frame too short (%d bytes)", len(frame))
	}
	if frame[0] != 0x68 || frame[3] != 0x68 || frame[len(frame)-1] != 0x16 {
		return nil, fmt.Errorf("invalid long-frame markers")
	}
	L := int(frame[1])
	if frame[2] != frame[1] {
		return nil, fmt.Errorf("frame length bytes mismatch")
	}
	if 4+L+2 > len(frame) {
		return nil, fmt.Errorf("frame length field inconsistent (L=%d, total=%d)", L, len(frame))
	}
	// Long frame layout inside frame[4:4+L]: C A CI data...
	// (CS and 0x16 are outside this slice)
	payload := frame[4 : 4+L]
	if len(payload) < 3 {
		return nil, fmt.Errorf("payload too short (%d bytes, need at least 3 for C+A+CI)", len(payload))
	}

	ci := payload[2] // skip C (control) and A (address) bytes
	switch ci {
	case 0x72: // Variable data structure (most common)
		return decodeVariable(payload[3:])
	default:
		// CI not supported — return nil without error so caller can still output raw bytes
		return nil, nil
	}
}

func decodeVariable(data []byte) (*DecodedFrame, error) {
	// Fixed header: ID(4) + Mfr(2) + Version(1) + Medium(1) + AccessNo(1) + Status(1) + Signature(2) = 12 bytes
	if len(data) < 12 {
		return nil, fmt.Errorf("variable frame header too short (%d bytes, need 12)", len(data))
	}
	df := &DecodedFrame{
		ID:           decodeBCDID(data[0:4]),
		Manufacturer: decodeMfr(data[4], data[5]),
		Version:      data[6],
		Medium:       decodeMedium(data[7]),
		AccessNo:     data[8],
		Status:       data[9],
		// data[10:12] = signature (usually 0x0000)
	}
	// Ignore parse errors — return whatever records we managed to decode
	df.Records, _ = parseRecords(data[12:])
	return df, nil
}

// decodeBCDID converts 4 little-endian BCD bytes to an 8-digit decimal string.
// e.g. bytes [0x78, 0x56, 0x34, 0x12] → "12345678"
func decodeBCDID(b []byte) string {
	return fmt.Sprintf("%02X%02X%02X%02X", b[3], b[2], b[1], b[0])
}

// decodeMfr decodes a 2-byte manufacturer ID to a 3-letter ASCII code.
func decodeMfr(lo, hi byte) string {
	word := uint16(lo) | uint16(hi)<<8
	c1 := byte((word>>10)&0x1F) + 64
	c2 := byte((word>>5)&0x1F) + 64
	c3 := byte(word&0x1F) + 64
	return string([]byte{c1, c2, c3})
}

func decodeMedium(m byte) string {
	switch m {
	case 0x00:
		return "other"
	case 0x01:
		return "oil"
	case 0x02:
		return "electricity"
	case 0x03:
		return "gas"
	case 0x04:
		return "heat_outlet"
	case 0x05:
		return "steam"
	case 0x06:
		return "hot_water"
	case 0x07:
		return "water"
	case 0x08:
		return "hca"
	case 0x09:
		return "compressed_air"
	case 0x0A:
		return "cooling_load_outlet"
	case 0x0B:
		return "cooling_load_inlet"
	case 0x0C:
		return "heat_inlet"
	case 0x0D:
		return "heat_cooling_load"
	case 0x0E:
		return "bus_system"
	case 0x0F:
		return "unknown_medium"
	case 0x15:
		// Not in EN 13757-3 but widely used by vendors for warm water
		return "warm_water"
	case 0x16:
		return "cold_water"
	case 0x17:
		return "dual_water"
	case 0x18:
		return "pressure"
	case 0x19:
		return "ad_converter"
	default:
		return fmt.Sprintf("0x%02X", m)
	}
}

// vifDataType describes how the raw data bytes should be interpreted.
type vifDataType int

const (
	vifDataNormal   vifDataType = iota // numeric (int or float) with multiplier
	vifDataDate                        // EN 13757-3 Type G: 2-byte date
	vifDataDatetime                    // EN 13757-3 Type F: 4-byte date+time
	vifDataString                      // ASCII string stored in reverse byte order
)

// vifInfo describes what a VIF byte means.
type vifInfo struct {
	quantity   string
	unit       string
	multiplier float64
	dataType   vifDataType
}

// decodeVIF maps a VIF byte (bit 7 stripped) to a vifInfo.
func decodeVIF(vif byte) vifInfo {
	v := vif & 0x7F
	n := int(v & 0x07)
	switch {
	case v <= 0x07:
		return vifInfo{"energy", "Wh", math.Pow10(n - 3), vifDataNormal}
	case v <= 0x0F:
		return vifInfo{"energy", "J", math.Pow10(n), vifDataNormal}
	case v <= 0x17:
		return vifInfo{"volume", "m3", math.Pow10(n - 6), vifDataNormal}
	case v <= 0x1F:
		return vifInfo{"mass", "kg", math.Pow10(n - 3), vifDataNormal}
	case v <= 0x23:
		return vifInfo{"on_time", timeUnit(v & 0x03), 1, vifDataNormal}
	case v <= 0x27:
		return vifInfo{"operating_time", timeUnit(v & 0x03), 1, vifDataNormal}
	case v <= 0x2F:
		return vifInfo{"power", "W", math.Pow10(n - 3), vifDataNormal}
	case v <= 0x37:
		return vifInfo{"power", "J/h", math.Pow10(n), vifDataNormal}
	case v <= 0x3F:
		return vifInfo{"volume_flow", "m3/h", math.Pow10(n - 6), vifDataNormal}
	case v <= 0x47:
		return vifInfo{"volume_flow", "m3/min", math.Pow10(n - 7), vifDataNormal}
	case v <= 0x4F:
		return vifInfo{"volume_flow", "m3/s", math.Pow10(n - 9), vifDataNormal}
	case v <= 0x57:
		return vifInfo{"mass_flow", "kg/h", math.Pow10(n - 3), vifDataNormal}
	case v <= 0x5B:
		return vifInfo{"flow_temperature", "°C", math.Pow10((n & 0x03) - 3), vifDataNormal}
	case v <= 0x5F:
		return vifInfo{"return_temperature", "°C", math.Pow10((n & 0x03) - 3), vifDataNormal}
	case v <= 0x63:
		return vifInfo{"temperature_difference", "K", math.Pow10((n & 0x03) - 3), vifDataNormal}
	case v <= 0x67:
		return vifInfo{"external_temperature", "°C", math.Pow10((n & 0x03) - 3), vifDataNormal}
	case v <= 0x6B:
		return vifInfo{"pressure", "bar", math.Pow10((n & 0x03) - 3), vifDataNormal}
	case v == 0x6C:
		return vifInfo{"time_point", "date", 1, vifDataDate}
	case v == 0x6D:
		return vifInfo{"time_point", "datetime", 1, vifDataDatetime}
	case v == 0x6E:
		return vifInfo{"hca", "", 1, vifDataNormal}
	case v == 0x6F:
		return vifInfo{"reserved", "", 1, vifDataNormal}
	case v <= 0x73:
		return vifInfo{"averaging_duration", timeUnit(v & 0x03), 1, vifDataNormal}
	case v <= 0x77:
		return vifInfo{"actuality_duration", timeUnit(v & 0x03), 1, vifDataNormal}
	case v == 0x78:
		return vifInfo{"fabrication_no", "", 1, vifDataNormal}
	case v == 0x79:
		return vifInfo{"enhanced_id", "", 1, vifDataNormal}
	case v == 0x7A:
		return vifInfo{"bus_address", "", 1, vifDataNormal}
	case v == 0x7C:
		return vifInfo{"text", "", 1, vifDataString}
	case v == 0x7E:
		return vifInfo{"any_vif", "", 1, vifDataNormal}
	case v == 0x7F:
		return vifInfo{"manufacturer_specific", "", 1, vifDataNormal}
	default:
		return vifInfo{"unknown", "", 1, vifDataNormal}
	}
}

// decodeFDVIFE looks up a secondary VIF from the EN 13757-3 Annex A FD-extension table.
// Called when the primary VIF byte is 0xFD; vife is the first VIFE byte (bit 7 may be set).
func decodeFDVIFE(vife byte) vifInfo {
	v := vife & 0x7F
	switch {
	case v == 0x00:
		return vifInfo{"credit", "currency", 1e-3, vifDataNormal}
	case v == 0x01:
		return vifInfo{"credit", "currency", 1e-2, vifDataNormal}
	case v == 0x02:
		return vifInfo{"credit", "currency", 1e-1, vifDataNormal}
	case v == 0x03:
		return vifInfo{"credit", "currency", 1, vifDataNormal}
	case v == 0x04:
		return vifInfo{"debit", "currency", 1e-3, vifDataNormal}
	case v == 0x05:
		return vifInfo{"debit", "currency", 1e-2, vifDataNormal}
	case v == 0x06:
		return vifInfo{"debit", "currency", 1e-1, vifDataNormal}
	case v == 0x07:
		return vifInfo{"debit", "currency", 1, vifDataNormal}
	case v == 0x08:
		return vifInfo{"access_number", "", 1, vifDataNormal}
	case v == 0x09:
		return vifInfo{"medium_info", "", 1, vifDataNormal}
	case v == 0x0A:
		return vifInfo{"manufacturer", "", 1, vifDataString}
	case v == 0x0B:
		return vifInfo{"parameter_set_id", "", 1, vifDataNormal}
	case v == 0x0C:
		return vifInfo{"model_version", "", 1, vifDataString}
	case v == 0x0D:
		return vifInfo{"hardware_version", "", 1, vifDataString}
	case v == 0x0E:
		return vifInfo{"firmware_version", "", 1, vifDataString}
	case v == 0x0F:
		return vifInfo{"software_version", "", 1, vifDataString}
	case v == 0x10:
		return vifInfo{"customer_location", "", 1, vifDataString}
	case v == 0x11:
		return vifInfo{"customer", "", 1, vifDataString}
	case v == 0x12:
		return vifInfo{"access_code_user", "", 1, vifDataNormal}
	case v == 0x13:
		return vifInfo{"access_code_operator", "", 1, vifDataNormal}
	case v == 0x14:
		return vifInfo{"access_code_system_operator", "", 1, vifDataNormal}
	case v == 0x15:
		return vifInfo{"access_code_developer", "", 1, vifDataNormal}
	case v == 0x16:
		return vifInfo{"password", "", 1, vifDataNormal}
	case v == 0x17:
		return vifInfo{"error_flags", "", 1, vifDataNormal}
	case v == 0x18:
		return vifInfo{"error_mask", "", 1, vifDataNormal}
	case v == 0x19:
		return vifInfo{"reserved", "", 1, vifDataNormal}
	case v == 0x1A:
		return vifInfo{"digital_output", "", 1, vifDataNormal}
	case v == 0x1B:
		return vifInfo{"digital_input", "", 1, vifDataNormal}
	case v == 0x1C:
		return vifInfo{"baudrate", "Baud", 1, vifDataNormal}
	case v == 0x1D:
		return vifInfo{"response_delay", "Bittimes", 1, vifDataNormal}
	case v == 0x1E:
		return vifInfo{"retry", "", 1, vifDataNormal}
	case v == 0x1F:
		return vifInfo{"reserved", "", 1, vifDataNormal}
	case v == 0x20:
		return vifInfo{"first_storage_no", "", 1, vifDataNormal}
	case v == 0x21:
		return vifInfo{"last_storage_no", "", 1, vifDataNormal}
	case v == 0x22:
		return vifInfo{"storage_block_size", "", 1, vifDataNormal}
	case v == 0x23:
		return vifInfo{"tariff_start", "", 1, vifDataNormal}
	case v == 0x24:
		return vifInfo{"storage_interval", "s", 1, vifDataNormal}
	case v == 0x25:
		return vifInfo{"storage_interval", "s", 60, vifDataNormal}
	case v == 0x26:
		return vifInfo{"storage_interval", "s", 3600, vifDataNormal}
	case v == 0x27:
		return vifInfo{"storage_interval", "s", 86400, vifDataNormal}
	case v == 0x28:
		return vifInfo{"storage_interval", "s", 2629744, vifDataNormal} // ~month
	case v == 0x29:
		return vifInfo{"storage_interval", "s", 31556926, vifDataNormal} // ~year
	case v == 0x2C:
		return vifInfo{"duration_since_readout", "s", 1, vifDataNormal}
	case v == 0x2D:
		return vifInfo{"duration_since_readout", "s", 60, vifDataNormal}
	case v == 0x2E:
		return vifInfo{"duration_since_readout", "s", 3600, vifDataNormal}
	case v == 0x2F:
		return vifInfo{"duration_since_readout", "s", 86400, vifDataNormal}
	case v == 0x30:
		return vifInfo{"duration_of_tariff", "s", 1, vifDataNormal}
	case v == 0x31:
		return vifInfo{"duration_of_tariff", "s", 60, vifDataNormal}
	case v == 0x32:
		return vifInfo{"duration_of_tariff", "s", 3600, vifDataNormal}
	case v == 0x33:
		return vifInfo{"duration_of_tariff", "s", 86400, vifDataNormal}
	case v == 0x34:
		return vifInfo{"period_of_tariff", "s", 1, vifDataNormal}
	case v == 0x35:
		return vifInfo{"period_of_tariff", "s", 60, vifDataNormal}
	case v == 0x36:
		return vifInfo{"period_of_tariff", "s", 3600, vifDataNormal}
	case v == 0x37:
		return vifInfo{"period_of_tariff", "s", 86400, vifDataNormal}
	case v == 0x38:
		return vifInfo{"period_of_tariff", "s", 2629744, vifDataNormal} // ~month
	case v == 0x39:
		return vifInfo{"period_of_tariff", "s", 31556926, vifDataNormal} // ~year
	case v == 0x3A:
		return vifInfo{"dimensionless", "", 1, vifDataNormal}
	case v >= 0x40 && v <= 0x4F:
		// E100 nnnn: Voltage × 10^(nnnn-9) V
		return vifInfo{"voltage", "V", math.Pow10(int(v&0x0F) - 9), vifDataNormal}
	case v >= 0x50 && v <= 0x5F:
		// E101 nnnn: Current × 10^(nnnn-12) A
		return vifInfo{"current", "A", math.Pow10(int(v&0x0F) - 12), vifDataNormal}
	case v == 0x60:
		return vifInfo{"reset_counter", "", 1, vifDataNormal}
	case v == 0x61:
		return vifInfo{"cumulation_counter", "", 1, vifDataNormal}
	case v == 0x62:
		return vifInfo{"control_signal", "", 1, vifDataNormal}
	case v == 0x63:
		return vifInfo{"day_of_week", "", 1, vifDataNormal}
	case v == 0x64:
		return vifInfo{"week_number", "", 1, vifDataNormal}
	case v == 0x65:
		return vifInfo{"day_change_time", "", 1, vifDataNormal}
	case v == 0x66:
		return vifInfo{"parameter_activation_state", "", 1, vifDataNormal}
	case v == 0x67:
		return vifInfo{"supplier_info", "", 1, vifDataNormal}
	case v == 0x68:
		return vifInfo{"duration_since_cumulation", "h", 1, vifDataNormal}
	case v == 0x69:
		return vifInfo{"duration_since_cumulation", "d", 1, vifDataNormal}
	case v == 0x6A:
		return vifInfo{"duration_since_cumulation", "months", 1, vifDataNormal}
	case v == 0x6B:
		return vifInfo{"duration_since_cumulation", "years", 1, vifDataNormal}
	case v == 0x6C:
		return vifInfo{"battery_operating_time", "h", 1, vifDataNormal}
	case v == 0x6D:
		return vifInfo{"battery_operating_time", "d", 1, vifDataNormal}
	case v == 0x6E:
		return vifInfo{"battery_operating_time", "months", 1, vifDataNormal}
	case v == 0x6F:
		return vifInfo{"battery_operating_time", "years", 1, vifDataNormal}
	case v == 0x70:
		return vifInfo{"battery_change_datetime", "", 1, vifDataNormal}
	case v == 0x7F:
		return vifInfo{"manufacturer_specific", "", 1, vifDataNormal}
	default:
		return vifInfo{"unknown", "", 1, vifDataNormal}
	}
}

// decodeFBVIFE looks up a secondary VIF from the EN 13757-3 Annex C FB-extension table.
// Called when the primary VIF byte is 0xFB; vife is the first VIFE byte (bit 7 may be set).
func decodeFBVIFE(vife byte) vifInfo {
	v := vife & 0x7F
	switch {
	case v == 0x00:
		return vifInfo{"energy", "Wh", 1e5, vifDataNormal}
	case v == 0x01:
		return vifInfo{"energy", "Wh", 1e6, vifDataNormal}
	case v == 0x08:
		return vifInfo{"energy", "J", 1e8, vifDataNormal}
	case v == 0x09:
		return vifInfo{"energy", "J", 1e9, vifDataNormal}
	case v == 0x10:
		return vifInfo{"volume", "m3", 1e2, vifDataNormal}
	case v == 0x11:
		return vifInfo{"volume", "m3", 1e3, vifDataNormal}
	case v == 0x18:
		return vifInfo{"mass", "kg", 1e5, vifDataNormal}
	case v == 0x19:
		return vifInfo{"mass", "kg", 1e6, vifDataNormal}
	case v == 0x21:
		return vifInfo{"volume", "ft3", 0.1, vifDataNormal}
	case v == 0x22:
		return vifInfo{"volume", "gal", 0.1, vifDataNormal}
	case v == 0x23:
		return vifInfo{"volume", "gal", 1, vifDataNormal}
	case v == 0x24:
		return vifInfo{"volume_flow", "gal/min", 1e-3, vifDataNormal}
	case v == 0x25:
		return vifInfo{"volume_flow", "gal/min", 1, vifDataNormal}
	case v == 0x26:
		return vifInfo{"volume_flow", "gal/d", 1, vifDataNormal}
	case v == 0x28:
		return vifInfo{"power", "W", 1e5, vifDataNormal}
	case v == 0x29:
		return vifInfo{"power", "W", 1e6, vifDataNormal}
	case v == 0x30:
		return vifInfo{"power", "J/h", 1e8, vifDataNormal}
	case v == 0x31:
		return vifInfo{"power", "J/h", 1e9, vifDataNormal}
	case v >= 0x58 && v <= 0x5B:
		return vifInfo{"flow_temperature", "°F", math.Pow10(int(v&0x03) - 3), vifDataNormal}
	case v >= 0x5C && v <= 0x5F:
		return vifInfo{"return_temperature", "°F", math.Pow10(int(v&0x03) - 3), vifDataNormal}
	case v >= 0x60 && v <= 0x63:
		return vifInfo{"temperature_difference", "°F", math.Pow10(int(v&0x03) - 3), vifDataNormal}
	case v >= 0x64 && v <= 0x67:
		return vifInfo{"external_temperature", "°F", math.Pow10(int(v&0x03) - 3), vifDataNormal}
	case v >= 0x70 && v <= 0x73:
		return vifInfo{"temperature_limit", "°F", math.Pow10(int(v&0x03) - 3), vifDataNormal}
	case v >= 0x74 && v <= 0x77:
		return vifInfo{"temperature_limit", "°C", math.Pow10(int(v&0x03) - 3), vifDataNormal}
	case v >= 0x78 && v <= 0x7F:
		return vifInfo{"cumul_max_power", "W", math.Pow10(int(v&0x07) - 3), vifDataNormal}
	default:
		return vifInfo{"unknown", "", 1, vifDataNormal}
	}
}

func timeUnit(n byte) string {
	return []string{"s", "min", "h", "days"}[n&0x03]
}

func parseRecords(data []byte) ([]Record, error) {
	var records []Record
	pos := 0
	for pos < len(data) {
		b := data[pos]
		// Skip idle filler
		if b == 0x2F {
			pos++
			continue
		}
		// Manufacturer-specific or end-of-data markers
		if b == 0x0F || b == 0x1F {
			break
		}

		dif := b
		pos++

		// Data function (DIF bits 4-5)
		var dataFunction string
		switch (dif >> 4) & 0x03 {
		case 0:
			dataFunction = "instantaneous"
		case 1:
			dataFunction = "maximum"
		case 2:
			dataFunction = "minimum"
		case 3:
			dataFunction = "error_value"
		}

		// Data field (bits 0-3)
		df := dif & 0x0F
		isBCD := df >= 0x09 && df <= 0x0C || df == 0x0E
		isFloat := df == 0x05
		isVariable := df == 0x0D

		dataLen := [16]int{0, 1, 2, 3, 4, 4, 6, 8, 0, 1, 2, 3, 4, 0, 6, 0}[df]

		// Storage number: LSB from DIF bit 6, then 4 bits per DIFE
		storageNo := uint64((dif >> 6) & 0x01)
		tariff := uint(0)
		storageShift := uint(1)
		tariffShift := uint(0)
		cur := dif
		for (cur & 0x80) != 0 {
			if pos >= len(data) {
				break
			}
			dife := data[pos]
			pos++
			storageNo |= uint64((dife>>1)&0x0F) << storageShift
			storageShift += 4
			tariff |= uint((dife>>5)&0x03) << tariffShift
			tariffShift += 2
			cur = dife
		}

		// VIF
		if pos >= len(data) {
			break
		}
		vif := data[pos]
		pos++

		var vi vifInfo
		var vifRaw string

		if vif == 0xFD {
			// FD-extension: secondary VIF from EN 13757-3 Annex A.
			if pos >= len(data) {
				break
			}
			secondaryVIF := data[pos]
			pos++
			vi = decodeFDVIFE(secondaryVIF)
			if vi.quantity == "unknown" {
				vifRaw = fmt.Sprintf("0xFD/0x%02X", secondaryVIF&0x7F)
			}
			// Consume remaining VIFE chain (bit 7 means more VIFEs follow).
			cur = secondaryVIF
			for cur&0x80 != 0 {
				if pos >= len(data) {
					break
				}
				cur = data[pos]
				pos++
			}
		} else if vif == 0xFB {
			// FB-extension: secondary VIF from EN 13757-3 Annex C.
			if pos >= len(data) {
				break
			}
			secondaryVIF := data[pos]
			pos++
			vi = decodeFBVIFE(secondaryVIF)
			if vi.quantity == "unknown" {
				vifRaw = fmt.Sprintf("0xFB/0x%02X", secondaryVIF&0x7F)
			}
			// Consume remaining VIFE chain.
			cur = secondaryVIF
			for cur&0x80 != 0 {
				if pos >= len(data) {
					break
				}
				cur = data[pos]
				pos++
			}
		} else {
			vi = decodeVIF(vif)
			if vi.quantity == "unknown" {
				vifRaw = fmt.Sprintf("0x%02X", vif&0x7F)
			}
			// Consume VIFE bytes (modifiers we don't further decode).
			cur = vif
			for cur&0x80 != 0 {
				if pos >= len(data) {
					break
				}
				cur = data[pos]
				pos++
			}
		}

		// Variable-length data: next byte is the length
		if isVariable {
			if pos >= len(data) {
				break
			}
			dataLen = int(data[pos])
			pos++
		}

		if pos+dataLen > len(data) {
			break
		}
		raw := data[pos : pos+dataLen]
		pos += dataLen

		val, err := decodeValue(raw, isBCD, isFloat, vi)

		rec := Record{
			DataFunction: dataFunction,
			Quantity:     vi.quantity,
			Unit:         vi.unit,
			Value:        val,
			VIFRaw:       vifRaw,
			StorageNo:    storageNo,
			Tariff:       tariff,
		}
		if err != nil {
			rec.Error = fmt.Sprintf("0x%X: %v", raw, err)
			rec.Value = nil
		}
		records = append(records, rec)
	}
	return records, nil
}

func decodeValue(b []byte, isBCD, isFloat bool, vi vifInfo) (any, error) {
	if len(b) == 0 {
		return nil, nil
	}

	switch vi.dataType {
	case vifDataString:
		// ASCII string stored in reverse byte order
		s := make([]byte, len(b))
		for i, c := range b {
			s[len(b)-1-i] = c
		}
		return strings.TrimRight(string(s), "\x00"), nil
	case vifDataDate:
		return decodeTypeGDate(b), nil
	case vifDataDatetime:
		return decodeTypeFDatetime(b), nil
	}

	if isBCD {
		raw := decodeBCD(b)
		if vi.multiplier != 1 {
			return float64(raw) * vi.multiplier, nil
		}
		return raw, nil
	}

	if isFloat {
		if len(b) < 4 {
			return nil, fmt.Errorf("need 4 bytes for float32, got %d", len(b))
		}
		bits := binary.LittleEndian.Uint32(b)
		f := math.Float32frombits(bits)
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return nil, fmt.Errorf("non-finite float32: %08X", bits)
		}
		return float64(f) * vi.multiplier, nil
	}

	// Signed integer, little-endian
	raw, err := decodeSigned(b)
	if err != nil {
		return nil, err
	}
	if vi.multiplier != 1 {
		return float64(raw) * vi.multiplier, nil
	}
	return raw, nil
}

func decodeSigned(b []byte) (int64, error) {
	switch len(b) {
	case 1:
		return int64(int8(b[0])), nil
	case 2:
		return int64(int16(binary.LittleEndian.Uint16(b))), nil
	case 3:
		u := uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
		if u&0x800000 != 0 {
			u |= 0xFF000000 // sign-extend
		}
		return int64(int32(u)), nil
	case 4:
		return int64(int32(binary.LittleEndian.Uint32(b))), nil
	case 6:
		u := uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 |
			uint64(b[3])<<24 | uint64(b[4])<<32 | uint64(b[5])<<40
		if u&0x800000000000 != 0 {
			u |= 0xFFFF000000000000
		}
		return int64(u), nil
	case 8:
		return int64(binary.LittleEndian.Uint64(b)), nil
	default:
		return 0, fmt.Errorf("unsupported integer length %d", len(b))
	}
}

// decodeBCD converts little-endian BCD bytes to an integer.
// b[0] holds the least-significant two digits.
func decodeBCD(b []byte) int64 {
	var result int64
	multiplier := int64(1)
	for _, by := range b {
		result += int64(by&0x0F) * multiplier
		multiplier *= 10
		result += int64(by>>4) * multiplier
		multiplier *= 10
	}
	return result
}

// decodeTypeGDate decodes a 2-byte Type G date (EN 13757-3 §6.3).
// Bits 0-4: day, bits 5-8: month, bits 9-15: year (0-99, add 2000).
func decodeTypeGDate(b []byte) string {
	if len(b) < 2 {
		return fmt.Sprintf("0x%X", b)
	}
	word := binary.LittleEndian.Uint16(b)
	day := word & 0x1F
	month := (word >> 5) & 0x0F
	year := (word >> 9) & 0x7F
	return fmt.Sprintf("20%02d-%02d-%02d", year, month, day)
}

// decodeTypeFDatetime decodes a 4-byte Type F datetime (EN 13757-3 §6.3).
// Byte 0: minutes (bits 5-0), summer time (bit 6), invalid (bit 7).
// Byte 1: hours (bits 4-0).
// Byte 2: day (bits 4-0), year bits Y4-Y2 (bits 7-5).
// Byte 3: month (bits 3-0), year bits Y6-Y4 (bits 6-4), DST flag (bit 7).
func decodeTypeFDatetime(b []byte) string {
	if len(b) < 4 {
		return fmt.Sprintf("0x%X", b)
	}
	min := b[0] & 0x3F
	hour := b[1] & 0x1F
	day := b[2] & 0x1F
	month := b[3] & 0x0F
	// Year is 7 bits: low 3 from b[2] bits 7-5, high 4 from b[3] bits 6-4 (bit 7 of b[3] is DST flag).
	year := int((b[2]>>5)&0x07) | int((b[3]>>4)&0x07)<<3
	return fmt.Sprintf("20%02d-%02d-%02dT%02d:%02d", year, month, day, hour, min)
}
