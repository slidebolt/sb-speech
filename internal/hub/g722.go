package hub

// G.722 (ITU-T G.722, 64 kbit/s): 16 kHz wideband audio in 8 bits per pair of
// samples. This is a port of the widely used reference implementation by Steve
// Underwood (spandsp), itself based on the CMU 1993 code, which its authors
// placed in the public domain. Only the standard 64 kbit/s mode is supported.

type g722Band struct {
	s, sp, sz int
	r         [3]int
	a, ap     [3]int
	p         [3]int
	d         [7]int
	b, bp     [7]int
	sg        [7]int
	nb, det   int
}

type g722State struct {
	x    [24]int
	band [2]g722Band
}

func newG722State() g722State {
	var s g722State
	s.band[0].det = 32
	s.band[1].det = 8
	return s
}

var (
	g722QMF = [12]int{3, -11, 12, 32, -210, 951, 3876, -805, 362, -156, 53, -11}
	g722Q6  = [32]int{
		0, 35, 72, 110, 150, 190, 233, 276,
		323, 370, 422, 473, 530, 587, 650, 714,
		786, 858, 940, 1023, 1121, 1219, 1339, 1458,
		1612, 1765, 1980, 2195, 2557, 2919, 0, 0,
	}
	g722ILN = [32]int{
		0, 63, 62, 31, 30, 29, 28, 27,
		26, 25, 24, 23, 22, 21, 20, 19,
		18, 17, 16, 15, 14, 13, 12, 11,
		10, 9, 8, 7, 6, 5, 4, 0,
	}
	g722ILP = [32]int{
		0, 61, 60, 59, 58, 57, 56, 55,
		54, 53, 52, 51, 50, 49, 48, 47,
		46, 45, 44, 43, 42, 41, 40, 39,
		38, 37, 36, 35, 34, 33, 32, 0,
	}
	g722WL   = [8]int{-60, -30, 58, 172, 334, 538, 1198, 3042}
	g722RL42 = [16]int{0, 7, 6, 5, 4, 3, 2, 1, 7, 6, 5, 4, 3, 2, 1, 0}
	g722ILB  = [32]int{
		2048, 2093, 2139, 2186, 2233, 2282, 2332,
		2383, 2435, 2489, 2543, 2599, 2656, 2714,
		2774, 2834, 2896, 2960, 3025, 3091, 3158,
		3228, 3298, 3371, 3444, 3520, 3597, 3676,
		3756, 3838, 3922, 4008,
	}
	g722QM4 = [16]int{
		0, -20456, -12896, -8968,
		-6288, -4240, -2584, -1200,
		20456, 12896, 8968, 6288,
		4240, 2584, 1200, 0,
	}
	g722QM6 = [64]int{
		-136, -136, -136, -136,
		-24808, -21904, -19008, -16704,
		-14984, -13512, -12280, -11192,
		-10232, -9360, -8576, -7856,
		-7192, -6576, -6000, -5456,
		-4944, -4464, -4008, -3576,
		-3168, -2776, -2400, -2032,
		-1688, -1360, -1040, -728,
		24808, 21904, 19008, 16704,
		14984, 13512, 12280, 11192,
		10232, 9360, 8576, 7856,
		7192, 6576, 6000, 5456,
		4944, 4464, 4008, 3576,
		3168, 2776, 2400, 2032,
		1688, 1360, 1040, 728,
		432, 136, -432, -136,
	}
	g722QM2 = [4]int{-7408, -1616, 7408, 1616}
	g722IHN = [3]int{0, 1, 0}
	g722IHP = [3]int{0, 3, 2}
	g722WH  = [3]int{0, -214, 798}
	g722RH2 = [4]int{2, 1, 2, 1}
)

func saturate(v int) int {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return v
}

func boolSel(c bool, a, b int) int {
	if c {
		return a
	}
	return b
}

// block4 updates a band's predictor after a quantized difference d.
func (st *g722State) block4(band, d int) {
	b := &st.band[band]
	// RECONS, PARREC
	b.d[0] = d
	b.r[0] = saturate(b.s + d)
	b.p[0] = saturate(b.sz + d)

	// UPPOL2
	for i := range 3 {
		b.sg[i] = b.p[i] >> 15
	}
	wd1 := saturate(b.a[1] << 2)
	wd2 := boolSel(b.sg[0] == b.sg[1], -wd1, wd1)
	if wd2 > 32767 {
		wd2 = 32767
	}
	wd3 := (wd2 >> 7) + boolSel(b.sg[0] == b.sg[2], 128, -128)
	wd3 += (b.a[2] * 32512) >> 15
	wd3 = min(max(wd3, -12288), 12288)
	b.ap[2] = wd3

	// UPPOL1
	b.sg[0] = b.p[0] >> 15
	b.sg[1] = b.p[1] >> 15
	wd1 = boolSel(b.sg[0] == b.sg[1], 192, -192)
	wd2 = (b.a[1] * 32640) >> 15
	b.ap[1] = saturate(wd1 + wd2)
	wd3 = saturate(15360 - b.ap[2])
	if b.ap[1] > wd3 {
		b.ap[1] = wd3
	} else if b.ap[1] < -wd3 {
		b.ap[1] = -wd3
	}

	// UPZERO
	wd1 = boolSel(d == 0, 0, 128)
	b.sg[0] = d >> 15
	for i := 1; i < 7; i++ {
		b.sg[i] = b.d[i] >> 15
		wd2 = boolSel(b.sg[i] == b.sg[0], wd1, -wd1)
		wd3 = (b.b[i] * 32640) >> 15
		b.bp[i] = saturate(wd2 + wd3)
	}

	// DELAYA
	for i := 6; i > 0; i-- {
		b.d[i] = b.d[i-1]
		b.b[i] = b.bp[i]
	}
	for i := 2; i > 0; i-- {
		b.r[i] = b.r[i-1]
		b.p[i] = b.p[i-1]
		b.a[i] = b.ap[i]
	}

	// FILTEP
	wd1 = saturate(b.r[1] + b.r[1])
	wd1 = (b.a[1] * wd1) >> 15
	wd2 = saturate(b.r[2] + b.r[2])
	wd2 = (b.a[2] * wd2) >> 15
	b.sp = saturate(wd1 + wd2)

	// FILTEZ
	b.sz = 0
	for i := 6; i > 0; i-- {
		wd1 = saturate(b.d[i] + b.d[i])
		b.sz += (b.b[i] * wd1) >> 15
	}
	b.sz = saturate(b.sz)

	// PREDIC
	b.s = saturate(b.sp + b.sz)
}

// scale returns the quantizer scale for a band's log scale factor nb.
func g722Scale(nb, shift int) int {
	wd1 := (nb >> 6) & 31
	wd2 := shift - (nb >> 11)
	var wd3 int
	if wd2 < 0 {
		wd3 = g722ILB[wd1] << -wd2
	} else {
		wd3 = g722ILB[wd1] >> wd2
	}
	return wd3 << 2
}

// G722Encoder encodes 16 kHz samples, two per output byte. It keeps state
// between calls, so one encoder serves one continuous stream.
type G722Encoder struct{ st g722State }

func NewG722Encoder() *G722Encoder { return &G722Encoder{st: newG722State()} }

// Encode appends the G.722 bytes for samples to dst. A trailing odd sample is
// ignored.
func (e *G722Encoder) Encode(dst []byte, samples []int16) []byte {
	st := &e.st
	for j := 0; j+1 < len(samples); j += 2 {
		// Transmit QMF: split into a low and a high band at 8 kHz each.
		copy(st.x[:22], st.x[2:])
		st.x[22] = int(samples[j])
		st.x[23] = int(samples[j+1])
		sumOdd, sumEven := 0, 0
		for i := range 12 {
			sumOdd += st.x[2*i] * g722QMF[i]
			sumEven += st.x[2*i+1] * g722QMF[11-i]
		}
		xlow := (sumEven + sumOdd) >> 14
		xhigh := (sumEven - sumOdd) >> 14

		// Low band: 6-bit ADPCM.
		lo := &st.band[0]
		el := saturate(xlow - lo.s)
		wd := boolSel(el >= 0, el, -(el + 1))
		i := 1
		for ; i < 30; i++ {
			if wd < (g722Q6[i]*lo.det)>>12 {
				break
			}
		}
		ilow := boolSel(el < 0, g722ILN[i], g722ILP[i])
		ril := ilow >> 2
		dlow := (lo.det * g722QM4[ril]) >> 15
		lo.nb = min(max((lo.nb*127)>>7+g722WL[g722RL42[ril]], 0), 18432)
		lo.det = g722Scale(lo.nb, 8)
		st.block4(0, dlow)

		// High band: 2-bit ADPCM.
		hi := &st.band[1]
		eh := saturate(xhigh - hi.s)
		wd = boolSel(eh >= 0, eh, -(eh + 1))
		mih := boolSel(wd >= (564*hi.det)>>12, 2, 1)
		ihigh := boolSel(eh < 0, g722IHN[mih], g722IHP[mih])
		dhigh := (hi.det * g722QM2[ihigh]) >> 15
		hi.nb = min(max((hi.nb*127)>>7+g722WH[g722RH2[ihigh]], 0), 22528)
		hi.det = g722Scale(hi.nb, 10)
		st.block4(1, dhigh)

		dst = append(dst, byte(ihigh<<6|ilow))
	}
	return dst
}

// G722Decoder decodes G.722 bytes to 16 kHz samples, two per byte. It keeps
// state between calls, so one decoder serves one continuous stream.
type G722Decoder struct{ st g722State }

func NewG722Decoder() *G722Decoder { return &G722Decoder{st: newG722State()} }

// Decode appends the samples for data to dst.
func (d *G722Decoder) Decode(dst []int16, data []byte) []int16 {
	st := &d.st
	for _, code := range data {
		c := int(code)
		ilow := c & 0x3f
		ihigh := (c >> 6) & 0x03

		// Low band.
		lo := &st.band[0]
		rlow := min(max(lo.s+(lo.det*g722QM6[ilow])>>15, -16384), 16383)
		ril := ilow >> 2
		dlow := (lo.det * g722QM4[ril]) >> 15
		lo.nb = min(max((lo.nb*127)>>7+g722WL[g722RL42[ril]], 0), 18432)
		lo.det = g722Scale(lo.nb, 8)
		st.block4(0, dlow)

		// High band.
		hi := &st.band[1]
		dhigh := (hi.det * g722QM2[ihigh]) >> 15
		rhigh := min(max(dhigh+hi.s, -16384), 16383)
		hi.nb = min(max((hi.nb*127)>>7+g722WH[g722RH2[ihigh]], 0), 22528)
		hi.det = g722Scale(hi.nb, 10)
		st.block4(1, dhigh)

		// Receive QMF: recombine the bands into two 16 kHz samples.
		copy(st.x[:22], st.x[2:])
		st.x[22] = rlow + rhigh
		st.x[23] = rlow - rhigh
		xout1, xout2 := 0, 0
		for i := range 12 {
			xout2 += st.x[2*i] * g722QMF[i]
			xout1 += st.x[2*i+1] * g722QMF[11-i]
		}
		dst = append(dst, int16(saturate(xout1>>11)), int16(saturate(xout2>>11)))
	}
	return dst
}
