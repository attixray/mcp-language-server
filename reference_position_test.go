package main

import (
	"math"
	"testing"
)

func TestReferencePositionArguments(t *testing.T) {
	for _, number := range []interface{}{1, float64(1)} {
		path, line, column, err := referencePositionArguments(map[string]interface{}{"filePath": "sample.cs", "line": number, "column": number})
		if err != nil || path != "sample.cs" || line != 1 || column != 1 {
			t.Fatalf("valid position: %q %d %d %v", path, line, column, err)
		}
	}
	for _, key := range []string{"line", "column"} {
		for _, invalid := range []interface{}{nil, "1", true, 0, -1, 1.5, math.NaN(), math.Inf(1), float64(math.MaxInt32) + 1} {
			args := map[string]interface{}{"filePath": "sample.cs", "line": 1, "column": 1}
			args[key] = invalid
			if _, _, _, err := referencePositionArguments(args); err == nil {
				t.Errorf("accepted invalid %s: %v", key, invalid)
			}
		}
	}
	for _, invalid := range []interface{}{nil, 1, "", "  "} {
		if _, _, _, err := referencePositionArguments(map[string]interface{}{"filePath": invalid, "line": 1, "column": 1}); err == nil {
			t.Errorf("accepted invalid filePath: %v", invalid)
		}
	}
}
