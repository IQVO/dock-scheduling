package shared_test

import (
	"errors"
	"testing"

	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

func TestNewGLN(t *testing.T) {
	tests := []struct {
		name  string
		input string
		ok    bool
	}{
		{"valid", "4006381333931", true},
		{"valid, check digit zero", "4012345000009", true},
		{"valid, from facility-layout's contract example", "9780201379624", true},
		{"valid, all zeros", "0000000000000", true},
		{"valid, check digit four", "9999999999994", true},
		{"wrong check digit", "4006381333932", false},
		{"check digit off by ten-complement", "4006381333930", false},
		{"two digits swapped (weights differ)", "4006381333391", false},
		{"first digit changed", "5006381333931", false},
		{"twelve digits", "400638133393", false},
		{"fourteen digits", "40063813339310", false},
		{"empty", "", false},
		{"letter first", "A006381333931", false},
		{"letter in the middle", "40063A1333931", false},
		{"letter last", "400638133393X", false},
		{"space inside", "4006381 33931", false},
		{"hyphenated", "4-006381-33393-1", false},
		{"non ASCII digit", "400638133393\u0661", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := shared.NewGLN(tc.input)
			if tc.ok {
				if err != nil || got.String() != tc.input {
					t.Fatalf("NewGLN(%q) = %q, %v; want it accepted unchanged", tc.input, got, err)
				}
				return
			}
			if !errors.Is(err, shared.ErrInvalidGLN) || got != "" {
				t.Fatalf("NewGLN(%q) = %q, %v; want ErrInvalidGLN", tc.input, got, err)
			}
		})
	}
}
