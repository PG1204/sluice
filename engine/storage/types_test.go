package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInferCellType(t *testing.T) {
	tests := []struct {
		in   string
		want Type
	}{
		{"0", TypeInt64},
		{"-42", TypeInt64},
		{"3.14", TypeFloat64},
		{"1e3", TypeFloat64},
		{"true", TypeBool},
		{"FALSE", TypeBool},
		{"hello", TypeString},
		{"2026-06-19", TypeString},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, inferCellType(tt.in))
		})
	}
}

func TestMergeTypes(t *testing.T) {
	tests := []struct {
		name string
		a, b Type
		want Type
	}{
		{"null widens to int", TypeNull, TypeInt64, TypeInt64},
		{"int widens to null other order", TypeInt64, TypeNull, TypeInt64},
		{"same stays", TypeInt64, TypeInt64, TypeInt64},
		{"int and float -> float", TypeInt64, TypeFloat64, TypeFloat64},
		{"float and int -> float", TypeFloat64, TypeInt64, TypeFloat64},
		{"bool and int -> string", TypeBool, TypeInt64, TypeString},
		{"string absorbs", TypeString, TypeFloat64, TypeString},
		{"bool and bool stays bool", TypeBool, TypeBool, TypeBool},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, mergeTypes(tt.a, tt.b))
		})
	}
}

func TestType_String(t *testing.T) {
	assert.Equal(t, "INT64", TypeInt64.String())
	assert.Equal(t, "FLOAT64", TypeFloat64.String())
	assert.Equal(t, "BOOL", TypeBool.String())
	assert.Equal(t, "STRING", TypeString.String())
	assert.Equal(t, "NULL", TypeNull.String())
}
