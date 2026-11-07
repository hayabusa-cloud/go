// Copyright 2026 Hayabusa Cloud Co., Ltd.
// Use of this source code is governed by a BSD-style license that can be found in the LICENSE file.

package atomix_test

import (
	"testing"

	"code.hybscloud.com/atomix"
)

func TestCompareExchange(t *testing.T) {
	var v uint64
	atomix.Store(&v, 41)
	if got := atomix.CompareExchange(&v, 41, 42); got != 41 || v != 42 {
		t.Fatalf("successful compare-exchange: old=%d value=%d", got, v)
	}
	if got := atomix.CompareExchange(&v, 41, 7); got != 42 || v != 42 {
		t.Fatalf("failed compare-exchange: old=%d value=%d", got, v)
	}
}

func TestRMW64(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		operation              func(*uint64, uint64) uint64
		initial, operand, want uint64
		stored                 uint64
	}{
		{"Add", atomix.Add, 0x100000029, 1, 0x10000002a, 0x10000002a},
		{"Or", atomix.Or, 0x100000010, 3, 0x100000010, 0x100000013},
		{"Xor", atomix.Xor, 0x100000013, 3, 0x100000013, 0x100000010},
		{"And", atomix.And, 0x100000013, 0x10000000f, 0x100000013, 0x100000003},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.initial
			if got := tc.operation(&v, tc.operand); got != tc.want || v != tc.stored {
				t.Fatalf("result=%#x value=%#x, want result=%#x value=%#x", got, v, tc.want, tc.stored)
			}
		})
	}
}

func TestRMW32(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		operation              func(*uint32, uint32) uint32
		initial, operand, want uint32
		stored                 uint32
	}{
		{"Add", atomix.Add32, 0x80000029, 1, 0x8000002a, 0x8000002a},
		{"Or", atomix.Or32, 0x80000010, 3, 0x80000010, 0x80000013},
		{"Xor", atomix.Xor32, 0x80000013, 3, 0x80000013, 0x80000010},
		{"And", atomix.And32, 0x80000013, 0x8000000f, 0x80000013, 0x80000003},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.initial
			if got := tc.operation(&v, tc.operand); got != tc.want || v != tc.stored {
				t.Fatalf("result=%#x value=%#x, want result=%#x value=%#x", got, v, tc.want, tc.stored)
			}
		})
	}
}

func TestIdentity64(t *testing.T) {
	for _, tc := range []struct {
		name      string
		operation func(*uint64) uint64
	}{
		{"Add", atomix.AddZero},
		{"Or", atomix.OrZero},
		{"Xor", atomix.XorZero},
		{"And", atomix.AndAllOnes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const want = uint64(0x8000000100000029)
			v := want
			if got := tc.operation(&v); got != want || v != want {
				t.Fatalf("result=%#x value=%#x, want %#x", got, v, want)
			}
		})
	}
}

func TestIdentity32(t *testing.T) {
	for _, tc := range []struct {
		name      string
		operation func(*uint32) uint32
	}{
		{"Add", atomix.AddZero32},
		{"Or", atomix.OrZero32},
		{"Xor", atomix.XorZero32},
		{"And", atomix.AndAllOnes32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const want = uint32(0x80000029)
			v := want
			if got := tc.operation(&v); got != want || v != want {
				t.Fatalf("result=%#x value=%#x, want %#x", got, v, want)
			}
		})
	}
}
