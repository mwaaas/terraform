package hcl2shim

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/zclconf/go-cty/cty/convert"

	"github.com/zclconf/go-cty/cty"
)

// FlatmapValueFromHCL2 converts a value from HCL2 (really, from the cty dynamic
// types library that HCL2 uses) to a map compatible with what would be
// produced by the "flatmap" package.
//
// The type of the given value informs the structure of the resulting map.
// The value must be of an object type or this function will panic.
//
// Flatmap values can only represent maps when they are of primitive types,
// so the given value must not have any maps of complex types or the result
// is undefined.
func FlatmapValueFromHCL2(v cty.Value) map[string]string {
	if !v.Type().IsObjectType() {
		panic(fmt.Sprintf("HCL2ValueFromFlatmap called on %#v", v.Type()))
	}

	m := make(map[string]string)
	// TODO: implement
	return m
}

// HCL2ValueFromFlatmap converts a map compatible with what would be produced
// by the "flatmap" package to a HCL2 (really, the cty dynamic types library
// that HCL2 uses) object type.
//
// The intended result type must be provided in order to guide how the
// map contents are decoded. This must be an object type or this function
// will panic.
//
// Flatmap values can only represent maps when they are of primitive types,
// so the given type must not have any maps of complex types or the result
// is undefined.
func HCL2ValueFromFlatmap(m map[string]string, ty cty.Type) (cty.Value, error) {
	if !ty.IsObjectType() {
		panic(fmt.Sprintf("HCL2ValueFromFlatmap called on %#v", ty))
	}

	return hcl2ValueFromFlatmapObject(m, "", ty.AttributeTypes())
}

func hcl2ValueFromFlatmapValue(m map[string]string, key string, ty cty.Type) (cty.Value, error) {
	var val cty.Value
	var err error
	switch {
	case ty.IsPrimitiveType():
		val, err = hcl2ValueFromFlatmapPrimitive(m, key, ty)
	case ty.IsObjectType():
		val, err = hcl2ValueFromFlatmapObject(m, key+".", ty.AttributeTypes())
	case ty.IsTupleType():
		val, err = hcl2ValueFromFlatmapTuple(m, key+".", ty.TupleElementTypes())
	case ty.IsMapType():
		val, err = hcl2ValueFromFlatmapMap(m, key+".", ty)
	case ty.IsListType() || ty.IsSetType():
		val, err = hcl2ValueFromFlatmapList(m, key+".", ty)
	default:
		err = fmt.Errorf("cannot decode %s from flatmap", ty.FriendlyName())
	}

	if err != nil {
		return cty.DynamicVal, err
	}
	return val, nil
}

func hcl2ValueFromFlatmapPrimitive(m map[string]string, key string, ty cty.Type) (cty.Value, error) {
	rawVal, exists := m[key]
	if !exists {
		return cty.NullVal(ty), nil
	}

	var err error
	val := cty.StringVal(rawVal)
	val, err = convert.Convert(val, ty)
	if err != nil {
		// This should never happen for _valid_ input, but flatmap data might
		// be tampered with by the user and become invalid.
		return cty.DynamicVal, fmt.Errorf("invalid value for %q in state: %s", key, err)
	}

	return val, nil
}

func hcl2ValueFromFlatmapObject(m map[string]string, prefix string, atys map[string]cty.Type) (cty.Value, error) {
	vals := make(map[string]cty.Value)
	for name, aty := range atys {
		val, err := hcl2ValueFromFlatmapValue(m, prefix+name, aty)
		if err != nil {
			return cty.DynamicVal, err
		}
		vals[name] = val
	}
	return cty.ObjectVal(vals), nil
}

func hcl2ValueFromFlatmapTuple(m map[string]string, prefix string, etys []cty.Type) (cty.Value, error) {
	var vals []cty.Value

	countStr, exists := m[prefix+"#"]
	if !exists {
		return cty.NullVal(cty.Tuple(etys)), nil
	}
	count, err := strconv.Atoi(countStr)
	if err != nil {
		return cty.DynamicVal, fmt.Errorf("invalid count value for %q in state: %s", prefix, err)
	}
	if count != len(etys) {
		return cty.DynamicVal, fmt.Errorf("wrong number of values for %q in state: got %d, but need %d", prefix, count, len(etys))
	}

	vals = make([]cty.Value, len(etys))
	for i, ety := range etys {
		key := prefix + strconv.Itoa(i)
		val, err := hcl2ValueFromFlatmapValue(m, key, ety)
		if err != nil {
			return cty.DynamicVal, err
		}
		vals[i] = val
	}
	return cty.TupleVal(vals), nil
}

func hcl2ValueFromFlatmapMap(m map[string]string, prefix string, ty cty.Type) (cty.Value, error) {
	vals := make(map[string]cty.Value)
	ety := ty.ElementType()

	for fullKey := range m {
		if !strings.HasPrefix(fullKey, prefix) {
			continue
		}

		// The flatmap format doesn't allow us to distinguish between keys
		// that contain periods and nested objects, so by convention a
		// map is only ever of primitive type in flatmap, and we just assume
		// that the remainder of the raw key (dots and all) is the key we
		// want in the result value.
		key := fullKey[len(prefix):]

		val, err := hcl2ValueFromFlatmapValue(m, key, ety)
		if err != nil {
			return cty.DynamicVal, err
		}
		vals[key] = val
	}

	if len(vals) == 0 {
		return cty.MapValEmpty(ety), nil
	}
	return cty.MapVal(vals), nil
}

func hcl2ValueFromFlatmapList(m map[string]string, prefix string, ty cty.Type) (cty.Value, error) {
	var vals []cty.Value

	countStr, exists := m[prefix+"#"]
	if !exists {
		return cty.NullVal(ty), nil
	}
	count, err := strconv.Atoi(countStr)
	if err != nil {
		return cty.DynamicVal, fmt.Errorf("invalid count value for %q in state: %s", prefix, err)
	}

	ety := ty.ElementType()
	if count == 0 {
		if ty.IsSetType() {
			return cty.SetValEmpty(ety), nil
		}
		return cty.ListValEmpty(ety), nil
	}

	vals = make([]cty.Value, count)
	for i := 0; i < count; i++ {
		key := prefix + strconv.Itoa(i)
		val, err := hcl2ValueFromFlatmapValue(m, key, ety)
		if err != nil {
			return cty.DynamicVal, err
		}
		vals[i] = val
	}

	if ty.IsSetType() {
		return cty.SetVal(vals), nil
	}
	return cty.ListVal(vals), nil
}
