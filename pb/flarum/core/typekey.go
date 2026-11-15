package core

import "reflect"

// typeKey returns the container key for a type.
//
// The key is the type's string form, which is unique per type within a
// program. Using the type itself as a map key would work too, but a string
// keeps the container's internals inspectable in tests and logs.
func typeKey[T any]() string {
	var zero T
	return reflect.TypeOf(&zero).Elem().String()
}
