package lintbad

import . "reflect"

// dotReflect names the unordered reflect methods through a dot import, as method expressions.
func dotReflect() {
	_ = Value.MapRange // want `unordered iteration: (reflect.Value).MapRange`
	_ = Value.MapKeys  // want `unordered iteration: (reflect.Value).MapKeys`
}
