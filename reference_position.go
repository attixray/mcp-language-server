package main

import (
	"fmt"
	"math"
	"strings"
)

func referencePositionArguments(args map[string]interface{}) (string, int, int, error) {
	path, ok := args["filePath"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return "", 0, 0, fmt.Errorf("filePath must be a non-empty string")
	}
	coordinate := func(name string) (int, error) {
		var value float64
		switch v := args[name].(type) {
		case float64:
			value = v
		case int:
			value = float64(v)
		default:
			return 0, fmt.Errorf("%s must be a positive 1-based integer", name)
		}
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 1 || value > math.MaxInt32 || math.Trunc(value) != value {
			return 0, fmt.Errorf("%s must be a positive 1-based integer no greater than 2147483647", name)
		}
		return int(value), nil
	}
	line, err := coordinate("line")
	if err != nil {
		return "", 0, 0, err
	}
	column, err := coordinate("column")
	return path, line, column, err
}
