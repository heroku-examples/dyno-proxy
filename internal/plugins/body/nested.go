package body

import (
	"fmt"
	"strings"
)

// GetNestedValue retrieves a value from a nested map using dot notation.
// Path segments are split on dots and traversed through the map structure.
//
// Examples:
//   - "organization.id" retrieves data["organization"]["id"]
//   - "total.profit" retrieves data["total"]["profit"]
//
// Returns:
//   - value: the found value (any type)
//   - found: true if the path was successfully traversed and value exists, false otherwise
//   - error: non-nil if there was an error during traversal (e.g., trying to traverse through a non-map)
func GetNestedValue(data map[string]any, path string) (any, bool, error) {
	// Split path on dots and validate segments
	segments := strings.Split(path, ".")
	for i, segment := range segments {
		segments[i] = strings.TrimSpace(segment)
		if segments[i] == "" {
			return nil, false, fmt.Errorf("invalid path: empty segment at position %d", i)
		}
	}

	// Traverse the nested structure
	var current any = data
	for i, segment := range segments {
		// Check if current value is a map
		currentMap, ok := current.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("cannot traverse through non-map at segment %d (%s): got type %T", i, segment, current)
		}

		// Get the value for this segment
		value, exists := currentMap[segment]
		if !exists {
			return nil, false, nil
		}

		// If this is the last segment, return the value
		if i == len(segments)-1 {
			return value, true, nil
		}

		// Otherwise, continue traversing
		current = value
	}

	// Should never reach here, but handle gracefully
	return nil, false, nil
}

// GetNestedValueAsString retrieves a nested value and converts it to a string.
// Uses the same dot notation traversal as GetNestedValue, then converts the result
// to a string using the same logic as extractFields.
//
// Type conversions:
//   - string: returned as-is
//   - float64: formatted as integer if whole number (e.g., "123"), otherwise as decimal (e.g., "123.45")
//   - bool: "true" or "false"
//   - nil: ""
//   - other types: fmt.Sprintf("%v", value)
//
// Returns:
//   - value: the string representation
//   - found: true if the path was successfully traversed and value exists, false otherwise
//   - error: non-nil if there was an error during traversal
func GetNestedValueAsString(data map[string]any, path string) (string, bool, error) {
	value, found, err := GetNestedValue(data, path)
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, nil
	}

	// Convert value to string using the same logic as extractFields
	var valueStr string
	switch v := value.(type) {
	case string:
		valueStr = v
	case float64:
		// JSON numbers are parsed as float64
		// Format as integer if it's a whole number, otherwise as float
		if v == float64(int64(v)) {
			valueStr = fmt.Sprintf("%d", int64(v))
		} else {
			valueStr = fmt.Sprintf("%g", v)
		}
	case bool:
		if v {
			valueStr = "true"
		} else {
			valueStr = "false"
		}
	case nil:
		valueStr = ""
	default:
		// For other types, convert to string
		valueStr = fmt.Sprintf("%v", v)
	}

	return valueStr, true, nil
}
