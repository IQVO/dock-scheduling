package shared

import (
	"errors"
	"strings"
)

// ErrInvalidGLN is returned for a GS1 Global Location Number that is not 13
// digits or whose last digit is not the GS1 mod-10 check digit of the first
// twelve.
var ErrInvalidGLN = errors.New("gln must be 13 digits whose last digit is the GS1 check digit of the first twelve")

// glnLength is the number of digits of a GLN.
const glnLength = 13

// GLN is a GS1 Global Location Number: the identity of a location (here a
// dock door) in the standard every partner reads. facility-layout owns it
// (its ADR 0045); this context only carries it, so a GLN is validated and
// never generated.
type GLN string

// NewGLN validates a GLN: exactly 13 ASCII digits and a correct GS1 check
// digit (weights 1 and 3 alternating from the left over the first twelve
// digits; the check digit completes the sum to a multiple of ten).
func NewGLN(value string) (GLN, error) {
	if len(value) != glnLength {
		return "", ErrInvalidGLN
	}
	sum := 0
	for i := 0; i < glnLength-1; i++ {
		d, ok := digit(value[i])
		if !ok {
			return "", ErrInvalidGLN
		}
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	check, ok := digit(value[glnLength-1])
	if !ok || (sum+check)%10 != 0 {
		return "", ErrInvalidGLN
	}
	return GLN(value), nil
}

// digit returns the value of an ASCII digit. (A lookup, not arithmetic on the
// byte: the mod-10 checksum is blind to a constant shift of every digit, so
// `b - '0'` would have an undetectable mutant.)
func digit(b byte) (int, bool) {
	i := strings.IndexByte("0123456789", b)
	return i, i >= 0
}

// String returns the 13 digits.
func (g GLN) String() string { return string(g) }
