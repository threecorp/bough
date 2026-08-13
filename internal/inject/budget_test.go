package inject

import "testing"

// The ceiling is maintained by arithmetic rather than a truncation pass, so
// something has to check the arithmetic. The assertion that did lived in the
// lessons test file and went with it; without one, raising DefaultBlockBytes
// past the total is a silent change to how much every prompt is billed.
func TestBlockBudgetStaysUnderTheCeiling(t *testing.T) {
	if DefaultBlockBytes > DefaultTotalBytes {
		t.Errorf("the instinct block's budget (%d) exceeds the total the hook may print (%d)",
			DefaultBlockBytes, DefaultTotalBytes)
	}
}
