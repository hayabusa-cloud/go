// Copyright 2026 Hayabusa Cloud Co., Ltd.
// Use of this source code is governed by a BSD-style license that can be found in the LICENSE file.

package atomix

import "code.hybscloud.com/atomix/internal/arch"

//go:noinline
func Add(p *uint64, v uint64) uint64 { return arch.AddUint64AcqRel(p, v) }

//go:noinline
func Or(p *uint64, v uint64) uint64 { return arch.OrUint64AcqRel(p, v) }

//go:noinline
func Xor(p *uint64, v uint64) uint64 { return arch.XorUint64AcqRel(p, v) }

//go:noinline
func And(p *uint64, v uint64) uint64 { return arch.AndUint64AcqRel(p, v) }

//go:noinline
func Add32(p *uint32, v uint32) uint32 { return arch.AddUint32AcqRel(p, v) }

//go:noinline
func Or32(p *uint32, v uint32) uint32 { return arch.OrUint32AcqRel(p, v) }

//go:noinline
func Xor32(p *uint32, v uint32) uint32 { return arch.XorUint32AcqRel(p, v) }

//go:noinline
func And32(p *uint32, v uint32) uint32 { return arch.AndUint32AcqRel(p, v) }

//go:noinline
func CompareExchange(p *uint64, old, new uint64) uint64 {
	return arch.CaxUint64AcqRel(p, old, new)
}

//go:noinline
func Store(p *uint64, v uint64) { arch.StoreUint64Release(p, v) }

// These wrappers exercise the fork's identity constant folding.

//go:noinline
func AddZero(p *uint64) uint64 { return arch.AddUint64AcqRel(p, 0) }

//go:noinline
func OrZero(p *uint64) uint64 { return arch.OrUint64AcqRel(p, 0) }

//go:noinline
func XorZero(p *uint64) uint64 { return arch.XorUint64AcqRel(p, 0) }

//go:noinline
func AndAllOnes(p *uint64) uint64 { return arch.AndUint64AcqRel(p, ^uint64(0)) }

//go:noinline
func AddZero32(p *uint32) uint32 { return arch.AddUint32AcqRel(p, 0) }

//go:noinline
func OrZero32(p *uint32) uint32 { return arch.OrUint32AcqRel(p, 0) }

//go:noinline
func XorZero32(p *uint32) uint32 { return arch.XorUint32AcqRel(p, 0) }

//go:noinline
func AndAllOnes32(p *uint32) uint32 { return arch.AndUint32AcqRel(p, ^uint32(0)) }
