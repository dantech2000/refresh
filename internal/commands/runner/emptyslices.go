package runner

import (
	"encoding"
	"encoding/json"
	"reflect"
)

var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

// emptySlices returns a copy of v in which every nil slice is an empty one,
// so -o json|yaml prints `[]` for an empty list instead of `null` (for
// example `status -o json` with no clusters). It walks structs, pointers,
// maps, slices, arrays, and interfaces. v itself is never modified. Byte
// slices and values with their own JSON or text encoding are left alone.
func emptySlices(v any) any {
	if v == nil {
		return nil
	}
	out := normalize(reflect.ValueOf(v))
	if !out.IsValid() {
		return v
	}
	return out.Interface()
}

func ownEncoding(t reflect.Type) bool {
	return t.Implements(jsonMarshalerType) || t.Implements(textMarshalerType) ||
		reflect.PointerTo(t).Implements(jsonMarshalerType) || reflect.PointerTo(t).Implements(textMarshalerType)
}

// normalize returns a copy of v with nil slices replaced by empty ones.
func normalize(v reflect.Value) reflect.Value {
	if !v.IsValid() {
		return v
	}
	t := v.Type()
	if ownEncoding(t) {
		return v
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		p := reflect.New(t.Elem())
		p.Elem().Set(normalize(v.Elem()))
		return p
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		inner := normalize(v.Elem())
		out := reflect.New(t).Elem()
		out.Set(inner)
		return out
	case reflect.Struct:
		out := reflect.New(t).Elem()
		out.Set(v)
		fillStruct(out)
		return out
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return v
		}
		if v.IsNil() {
			return reflect.MakeSlice(t, 0, 0)
		}
		out := reflect.MakeSlice(t, v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).Set(normalize(v.Index(i)))
		}
		return out
	case reflect.Array:
		out := reflect.New(t).Elem()
		for i := range v.Len() {
			out.Index(i).Set(normalize(v.Index(i)))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(t, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), normalize(iter.Value()))
		}
		return out
	default:
		return v
	}
}

// fillStruct normalizes the exported fields of the addressable struct s in
// place. Fields promoted from an unexported embedded struct are exported in
// JSON too, so it descends into those.
func fillStruct(s reflect.Value) {
	t := s.Type()
	for i := range t.NumField() {
		sf := t.Field(i)
		f := s.Field(i)
		switch {
		case sf.IsExported() && f.CanSet():
			f.Set(normalize(f))
		case sf.Anonymous && sf.Type.Kind() == reflect.Struct && !ownEncoding(sf.Type):
			fillStruct(f)
		}
	}
}
