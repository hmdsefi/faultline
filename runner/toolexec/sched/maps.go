// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package sched

import (
	"cmp"
	"iter"
	"reflect"
	"slices"
)

// shuffle permutes n elements with the user stream during an attempt; outside one it does nothing.
func shuffle(n int, swap func(i, j int)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active && s.user != nil {
		s.user.Shuffle(n, swap)
	}
}

// KeysCmp returns m's keys in compareValues' total order, then shuffled with the user stream.
// The rewriter passes only bool keys and struct or array keys of orderable fields.
func KeysCmp[K comparable, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m { //faultline:maporder sorted below
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b K) int { return compareValues(reflect.ValueOf(a), reflect.ValueOf(b)) })
	shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
	return keys
}

// compareValues orders two values of one type: false before true; integers, unsigned integers
// and strings naturally; structs field by field and arrays element by element.
func compareValues(a, b reflect.Value) int {
	switch a.Kind() {
	case reflect.Bool:
		return cmp.Compare(boolInt(a.Bool()), boolInt(b.Bool()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return cmp.Compare(a.Int(), b.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return cmp.Compare(a.Uint(), b.Uint())
	case reflect.String:
		return cmp.Compare(a.String(), b.String())
	case reflect.Struct:
		for i := range a.NumField() {
			if c := compareValues(a.Field(i), b.Field(i)); c != 0 {
				return c
			}
		}
	case reflect.Array:
		for i := range a.Len() {
			if c := compareValues(a.Index(i), b.Index(i)); c != 0 {
				return c
			}
		}
	}
	return 0
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// MapKeys replaces maps.Keys: KeysCmp's order, skipping keys deleted during the loop.
func MapKeys[K comparable, V any](m map[K]V) iter.Seq[K] {
	return func(yield func(K) bool) {
		for _, k := range KeysCmp(m) {
			if _, ok := m[k]; ok && !yield(k) {
				return
			}
		}
	}
}

// MapValues replaces maps.Values.
func MapValues[K comparable, V any](m map[K]V) iter.Seq[V] {
	return func(yield func(V) bool) {
		for _, k := range KeysCmp(m) {
			if v, ok := m[k]; ok && !yield(v) {
				return
			}
		}
	}
}

// MapAll replaces maps.All.
func MapAll[K comparable, V any](m map[K]V) iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for _, k := range KeysCmp(m) {
			if v, ok := m[k]; ok && !yield(k, v) {
				return
			}
		}
	}
}

type integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

// N replaces math/rand/v2.N: it panics for n <= 0 and draws as math/rand/v2.N does.
func N[Int integer](n Int) Int {
	if n <= 0 {
		panic("invalid argument to N")
	}
	return Int(Rand().Uint64N(uint64(n)))
}

// ReflectSelect replaces reflect.Select: a scheduling point, then Select over the communication
// cases, with the default case's index mapped back.
func ReflectSelect(cases []reflect.SelectCase) (chosen int, recv reflect.Value, recvOK bool) {
	Point()
	def := -1
	comm := make([]reflect.SelectCase, 0, len(cases))
	index := make([]int, 0, len(cases))
	for i, c := range cases {
		if c.Dir == reflect.SelectDefault {
			def = i
			continue
		}
		comm, index = append(comm, c), append(index, i)
	}
	c, v, ok := Select(comm, def >= 0)
	if c < 0 {
		return def, reflect.Value{}, false
	}
	return index[c], v, ok
}
