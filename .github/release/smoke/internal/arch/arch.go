// Copyright 2026 Hayabusa Cloud Co., Ltd.
// Use of this source code is governed by a BSD-style license that can be found in the LICENSE file.

package arch

// These declarations deliberately have no fallback definitions. Linking fails
// if the customized compiler stops recognizing the intrinsic names.

//go:noescape
func AddUint64AcqRel(p *uint64, v uint64) uint64

//go:noescape
func OrUint64AcqRel(p *uint64, v uint64) uint64

//go:noescape
func AndUint64AcqRel(p *uint64, v uint64) uint64

//go:noescape
func AddUint32AcqRel(p *uint32, v uint32) uint32

//go:noescape
func OrUint32AcqRel(p *uint32, v uint32) uint32

//go:noescape
func XorUint32AcqRel(p *uint32, v uint32) uint32

//go:noescape
func AndUint32AcqRel(p *uint32, v uint32) uint32

//go:noescape
func CaxUint64AcqRel(p *uint64, old, new uint64) uint64

//go:noescape
func XorUint64AcqRel(p *uint64, v uint64) uint64

//go:noescape
func StoreUint64Release(p *uint64, v uint64)
