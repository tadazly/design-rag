package biff

import "math"

// RK encoding (MS-XLS §2.5.5 – RkNumber):
//
//	Bit 0  fX100    if set, divide the decoded value by 100
//	Bit 1  fInt     if set, bits 31:2 are a signed 30-bit integer
//	                if clear, bits 31:2 are the high 30 bits of an IEEE-754 double
//	                (the low 34 bits of the double are implicitly 0)

// DecodeRK converts a 32-bit BIFF8 RK value to a float64.
func DecodeRK(rk uint32) float64 {
	var v float64
	if rk&0x02 != 0 {
		// Integer: arithmetic right-shift by 2 preserves the sign bit.
		v = float64(int32(rk) >> 2)
	} else {
		// Float: place bits 31:2 of rk into bits 63:34 of a double.
		bits := uint64(rk&0xFFFFFFFC) << 32
		v = math.Float64frombits(bits)
	}
	if rk&0x01 != 0 {
		v /= 100
	}
	return v
}

// EncodeRK attempts to encode f as a 32-bit BIFF8 RK value.
// Returns (rkValue, true) on success, (0, false) if f cannot be represented.
//
// All integers in the range [−536870912, 536870911] (30-bit signed) can be
// encoded exactly.  Many common floating-point values (e.g. 0.01, 0.5, 1.5)
// cannot be encoded as RK and should be written as NUMBER records instead.
func EncodeRK(f float64) (uint32, bool) {
	// Try integer encoding (with and without the ×100 flag).
	for _, x100 := range []bool{false, true} {
		v := f
		if x100 {
			v *= 100
		}
		// 30-bit signed range after the 2-bit shift.
		if v == math.Trunc(v) && v >= -536870912 && v <= 536870911 {
			rk := uint32(int32(v) << 2)
			rk |= 0x02 // fInt
			if x100 {
				rk |= 0x01
			}
			return rk, true
		}
	}

	// Try float encoding: the low 34 bits of the IEEE-754 double must be zero.
	for _, x100 := range []bool{false, true} {
		v := f
		if x100 {
			v *= 100
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		bits := math.Float64bits(v)
		if bits&0x0000003FFFFFFFF == 0 {
			rk := uint32(bits>>32) & 0xFFFFFFFC // clear bits 1:0
			if x100 {
				rk |= 0x01
			}
			return rk, true
		}
	}
	return 0, false
}
