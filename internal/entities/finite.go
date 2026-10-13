package entities

import (
	"math"
	"reflect"
)

// Finite replaces NaN and ±Inf inside *v, which encoding/json refuses to
// encode: a plain float becomes 0 and a *float64 becomes nil (no reading).
// It walks structs, slices, arrays and pointers; map values are left alone.
func Finite(v any) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return
	}
	finite(rv.Elem())
}

func finite(v reflect.Value) {
	switch v.Kind() {
	case reflect.Float32, reflect.Float64:
		if f := v.Float(); math.IsNaN(f) || math.IsInf(f, 0) {
			v.SetFloat(0)
		}
	case reflect.Pointer:
		if v.IsNil() {
			return
		}
		if k := v.Elem().Kind(); k == reflect.Float32 || k == reflect.Float64 {
			if f := v.Elem().Float(); math.IsNaN(f) || math.IsInf(f, 0) {
				v.SetZero()
			}
			return
		}
		finite(v.Elem())
	case reflect.Struct:
		for i := range v.NumField() {
			if f := v.Field(i); f.CanSet() {
				finite(f)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			finite(v.Index(i))
		}
	}
}
