package structmapper

import (
	"errors"
	"fmt"
	"reflect"
	"unicode"

	"encoding"

	"github.com/hashicorp/go-multierror"
)

// This file contains the map to struct functionality of Mapper

func (sm *Mapper) unmapPtr(in interface{}, out reflect.Value, t reflect.Type) error {
	child := reflect.New(t.Elem())
	if err := sm.unmapValue(in, child.Elem(), child.Elem().Type()); err != nil {
		return err
	}
	out.Set(child)
	return nil
}

func (sm *Mapper) unmapSlice(in interface{}, out reflect.Value, t reflect.Type) (err error) {
	inSlice := reflect.ValueOf(in)
	if inSlice.Kind() != reflect.Slice {
		return errors.New("Not a slice")
	}

	outSlice := reflect.MakeSlice(t, inSlice.Len(), inSlice.Cap())
	for i := 0; i < inSlice.Len(); i++ {
		inElem := inSlice.Index(i)
		outElem := outSlice.Index(i)

		inV := reflect.ValueOf(inElem.Interface())
		elemV := reflect.New(outElem.Type()).Elem()

		if unmapErr := sm.unmapValue(inV.Interface(), elemV, elemV.Type()); unmapErr != nil {
			err = multierror.Append(err, multierror.Prefix(unmapErr, fmt.Sprintf("@%d", i)))
			continue
		}

		outSlice.Index(i).Set(elemV)
	}

	if err == nil {
		out.Set(outSlice)
	}

	return
}

func (sm *Mapper) unmapMap(in interface{}, out reflect.Value, t reflect.Type) (err error) {
	inMap := reflect.ValueOf(in)
	if inMap.Kind() != reflect.Map {
		return errors.New("Not a map")
	}

	outMap := reflect.MakeMap(t)

	for _, keyElem := range inMap.MapKeys() {

		keyV := reflect.ValueOf(keyElem.Interface())
		key := keyV.Interface()
		outKey := reflect.New(keyV.Type()).Elem()
		if unmapErr := sm.unmapValue(key, outKey, outKey.Type()); unmapErr != nil {
			err = multierror.Append(err, multierror.Prefix(unmapErr, fmt.Sprintf("@%+v (key)", key)))
			continue
		}
		valueElem := inMap.MapIndex(keyV)
		valueV := reflect.ValueOf(valueElem.Interface())
		value := valueV.Interface()
		outValue := reflect.New(valueV.Type()).Elem()

		if unmapErr := sm.unmapValue(value, outValue, outValue.Type()); unmapErr != nil {
			err = multierror.Append(err, multierror.Prefix(unmapErr, fmt.Sprintf("@%+v (value)", key)))
			continue
		}

		outMap.SetMapIndex(outKey, outValue)
	}

	if err == nil {
		// Special case: out may be a struct or struct pointer...
		out.Set(outMap)
	}

	return
}

func (sm *Mapper) unmapArray(in interface{}, out reflect.Value, t reflect.Type) (err error) {
	inArray := reflect.ValueOf(in)

	if inArray.Kind() != reflect.Array && inArray.Kind() != reflect.Slice {
		return errors.New("Not an array or slice")
	}

	outArray := reflect.New(t).Elem()

	for i := 0; i < inArray.Len(); i++ {
		outElem := outArray.Index(i)
		inValue := inArray.Index(i)

		if unmapErr := sm.unmapValue(inValue.Interface(), outElem, outElem.Type()); unmapErr != nil {
			err = multierror.Append(err, multierror.Prefix(unmapErr, fmt.Sprintf("@%d", i)))
			continue
		}
	}

	if err == nil {
		out.Set(outArray)
	}

	return
}

func (sm *Mapper) unmapUnmarshal(in interface{}, out reflect.Value) (bool, error) {
	inString, ok := in.(string)
	if !ok {
		return false, nil
	}

	outI := out.Interface()

	if unmarshaler, ok := outI.(encoding.TextUnmarshaler); ok {
		return true, unmarshaler.UnmarshalText([]byte(inString))
	} else if out.CanAddr() {
		return sm.unmapUnmarshal(in, out.Addr())
	}

	return false, nil
}

func (sm *Mapper) unmapValue(in interface{}, out reflect.Value, t reflect.Type) error {
	// Check if the target implements encoding.TextUnmarshaler
	if handled, err := sm.unmapUnmarshal(in, out); handled {
		return err
	}

	switch out.Kind() {
	case reflect.Ptr:
		return sm.unmapPtr(in, out, t)
	case reflect.Struct:
		return sm.unmapStruct(in, out, t)
	case reflect.Slice:
		return sm.unmapSlice(in, out, t)
	case reflect.Map:
		return sm.unmapMap(in, out, t)
	case reflect.Array:
		return sm.unmapArray(in, out, t)

	}

	// Default case: copy the value over
	out.Set(reflect.ValueOf(in))
	return nil
}

func (sm *Mapper) unmapStruct(in interface{}, out reflect.Value, t reflect.Type) (err error) {
	if out.Kind() != reflect.Struct {
		return ErrNotAStruct
	}

	m, ok := in.(map[string]interface{})
	if !ok {
		return ErrInvalidMap
	}

	// Hold the values of the modified fields in a map, which will be applied shortly before
	// this function returns.
	// This ensures we do not modify the target struct at all in case of an error
	modifiedFields := make(map[int]reflect.Value, t.NumField())

	// Iterate over all fields of the passed struct
	for i := 0; i < out.NumField(); i++ {
		fieldD := t.Field(i)
		fieldV := out.Field(i)

		if fieldD.Anonymous {
			// Skip anonymous fields...
			continue
		}

		fieldName := fieldD.Name

		if !unicode.IsUpper([]rune(fieldName)[0]) {
			// Ignore private fields
			continue
		}

		tagValue := fieldD.Tag.Get(sm.tagName)

		// Handle the tag, if it was present
		if tagValue != "" {
			var tagErr error
			fieldName, _, tagErr = parseTag(tagValue)
			if tagErr != nil {
				// Parsing the tag failed, ignore the field and carry on
				err = multierror.Append(err, tagErr)
				continue
			}

			if fieldName == "-" {
				// Tag defines that the field shall be ignored, so carry on
				continue
			}
		}

		// Look up value of "fieldName" in map
		mapValue, ok := m[fieldName]
		if !ok {
			// Value not in map, ignore it
			continue
		}

		if fieldV.Kind() == reflect.Interface {
			// Setting interfaces is unsupported.
			err = multierror.Append(err, multierror.Prefix(ErrFieldIsInterface, fieldName+":"))
			continue
		}

		targetV := reflect.New(fieldD.Type).Elem()
		if unmapErr := sm.unmapValue(mapValue, targetV, fieldD.Type); unmapErr != nil {
			err = multierror.Append(err, multierror.Prefix(unmapErr, fieldName+":"))
			continue
		} else {
			modifiedFields[i] = targetV
		}
	}

	// Apply changes to all modified fields in case no error happened during processing.
	if err == nil {
		// Apply changes to all modified fields
		for fieldIndex, fieldValue := range modifiedFields {
			out.Field(fieldIndex).Set(fieldValue)
		}
	}
	return
}

func (sm *Mapper) toStruct(m map[string]interface{}, s interface{}) error {
	if m == nil {
		return ErrMapIsNil
	}

	v := reflect.ValueOf(s)
	if v.Kind() != reflect.Ptr {
		return ErrNotAStructPointer
	}

	v = v.Elem()

	return sm.unmapStruct(m, v, v.Type())
}