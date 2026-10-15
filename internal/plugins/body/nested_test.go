package body

import (
	"testing"
)

func TestGetNestedValue(t *testing.T) {
	tests := []struct {
		name      string
		data      map[string]any
		path      string
		wantValue any
		wantFound bool
		wantError bool
	}{
		{
			name: "simple nested path",
			data: map[string]any{
				"organization": map[string]any{
					"id": "org-123",
				},
			},
			path:      "organization.id",
			wantValue: "org-123",
			wantFound: true,
			wantError: false,
		},
		{
			name: "deeper nested path",
			data: map[string]any{
				"user": map[string]any{
					"profile": map[string]any{
						"contact": map[string]any{
							"email": "user@example.com",
						},
					},
				},
			},
			path:      "user.profile.contact.email",
			wantValue: "user@example.com",
			wantFound: true,
			wantError: false,
		},
		{
			name: "numeric value",
			data: map[string]any{
				"total": map[string]any{
					"profit": 123.45,
				},
			},
			path:      "total.profit",
			wantValue: 123.45,
			wantFound: true,
			wantError: false,
		},
		{
			name: "boolean value",
			data: map[string]any{
				"settings": map[string]any{
					"enabled": true,
				},
			},
			path:      "settings.enabled",
			wantValue: true,
			wantFound: true,
			wantError: false,
		},
		{
			name: "nil value",
			data: map[string]any{
				"data": map[string]any{
					"value": nil,
				},
			},
			path:      "data.value",
			wantValue: nil,
			wantFound: true,
			wantError: false,
		},
		{
			name: "missing key at top level",
			data: map[string]any{
				"foo": "bar",
			},
			path:      "nonexistent.id",
			wantValue: nil,
			wantFound: false,
			wantError: false,
		},
		{
			name: "missing key in nested path",
			data: map[string]any{
				"organization": map[string]any{
					"name": "test",
				},
			},
			path:      "organization.id",
			wantValue: nil,
			wantFound: false,
			wantError: false,
		},
		{
			name: "traversing through non-map (string)",
			data: map[string]any{
				"organization": "not-a-map",
			},
			path:      "organization.id",
			wantValue: nil,
			wantFound: false,
			wantError: true,
		},
		{
			name: "traversing through non-map (number)",
			data: map[string]any{
				"total": 123,
			},
			path:      "total.profit",
			wantValue: nil,
			wantFound: false,
			wantError: true,
		},
		{
			name: "traversing through non-map in middle of path",
			data: map[string]any{
				"user": map[string]any{
					"name": "John",
				},
			},
			path:      "user.name.first",
			wantValue: nil,
			wantFound: false,
			wantError: true,
		},
		{
			name: "empty segment in path",
			data: map[string]any{
				"foo": "bar",
			},
			path:      "organization..id",
			wantValue: nil,
			wantFound: false,
			wantError: true,
		},
		{
			name: "path with whitespace",
			data: map[string]any{
				"organization": map[string]any{
					"id": "org-123",
				},
			},
			path:      " organization . id ",
			wantValue: "org-123",
			wantFound: true,
			wantError: false,
		},
		{
			name: "single segment path",
			data: map[string]any{
				"name": "John",
			},
			path:      "name",
			wantValue: "John",
			wantFound: true,
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, found, err := GetNestedValue(tt.data, tt.path)

			if (err != nil) != tt.wantError {
				t.Errorf("GetNestedValue() error = %v, wantError %v", err, tt.wantError)
				return
			}

			if found != tt.wantFound {
				t.Errorf("GetNestedValue() found = %v, wantFound %v", found, tt.wantFound)
			}

			if found && value != tt.wantValue {
				t.Errorf("GetNestedValue() value = %v, wantValue %v", value, tt.wantValue)
			}
		})
	}
}

func TestGetNestedValueAsString(t *testing.T) {
	tests := []struct {
		name      string
		data      map[string]any
		path      string
		wantValue string
		wantFound bool
		wantError bool
	}{
		{
			name: "string value",
			data: map[string]any{
				"organization": map[string]any{
					"id": "org-123",
				},
			},
			path:      "organization.id",
			wantValue: "org-123",
			wantFound: true,
			wantError: false,
		},
		{
			name: "integer number",
			data: map[string]any{
				"user": map[string]any{
					"age": float64(30),
				},
			},
			path:      "user.age",
			wantValue: "30",
			wantFound: true,
			wantError: false,
		},
		{
			name: "decimal number",
			data: map[string]any{
				"total": map[string]any{
					"profit": 123.45,
				},
			},
			path:      "total.profit",
			wantValue: "123.45",
			wantFound: true,
			wantError: false,
		},
		{
			name: "boolean true",
			data: map[string]any{
				"settings": map[string]any{
					"enabled": true,
				},
			},
			path:      "settings.enabled",
			wantValue: "true",
			wantFound: true,
			wantError: false,
		},
		{
			name: "boolean false",
			data: map[string]any{
				"settings": map[string]any{
					"active": false,
				},
			},
			path:      "settings.active",
			wantValue: "false",
			wantFound: true,
			wantError: false,
		},
		{
			name: "nil value",
			data: map[string]any{
				"data": map[string]any{
					"value": nil,
				},
			},
			path:      "data.value",
			wantValue: "",
			wantFound: true,
			wantError: false,
		},
		{
			name: "missing key",
			data: map[string]any{
				"organization": map[string]any{
					"name": "test",
				},
			},
			path:      "organization.id",
			wantValue: "",
			wantFound: false,
			wantError: false,
		},
		{
			name: "traversing through non-map",
			data: map[string]any{
				"organization": "not-a-map",
			},
			path:      "organization.id",
			wantValue: "",
			wantFound: false,
			wantError: true,
		},
		{
			name: "large integer",
			data: map[string]any{
				"stats": map[string]any{
					"count": float64(1000000),
				},
			},
			path:      "stats.count",
			wantValue: "1000000",
			wantFound: true,
			wantError: false,
		},
		{
			name: "zero value",
			data: map[string]any{
				"metrics": map[string]any{
					"errors": float64(0),
				},
			},
			path:      "metrics.errors",
			wantValue: "0",
			wantFound: true,
			wantError: false,
		},
		{
			name: "deeply nested path",
			data: map[string]any{
				"level1": map[string]any{
					"level2": map[string]any{
						"level3": map[string]any{
							"level4": map[string]any{
								"value": "deep",
							},
						},
					},
				},
			},
			path:      "level1.level2.level3.level4.value",
			wantValue: "deep",
			wantFound: true,
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, found, err := GetNestedValueAsString(tt.data, tt.path)

			if (err != nil) != tt.wantError {
				t.Errorf("GetNestedValueAsString() error = %v, wantError %v", err, tt.wantError)
				return
			}

			if found != tt.wantFound {
				t.Errorf("GetNestedValueAsString() found = %v, wantFound %v", found, tt.wantFound)
			}

			if found && value != tt.wantValue {
				t.Errorf("GetNestedValueAsString() value = %q, wantValue %q", value, tt.wantValue)
			}
		})
	}
}
