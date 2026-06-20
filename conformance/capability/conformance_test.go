package capability

import (
	"testing"

	"github.com/ikeikeikeike/bough/internal/export"
	capapi "github.com/ikeikeikeike/bough/plugins/capability/api"
)

// TestSelf wires the three v0.6 builtin emitters (agent-skill +
// claude-skill + mcp) through the conformance suite. If the
// compile loop or DefaultRegistry ever drifts, this test surfaces
// the gap before any external compiler author notices.
func TestSelf(t *testing.T) {
	r := export.DefaultRegistry()
	formats := r.Formats()
	emitters := make([]capapi.Emitter, 0, len(formats))
	for _, f := range formats {
		e, err := r.Lookup(f)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", f, err)
		}
		emitters = append(emitters, e)
	}
	Run(t, Config{Emitters: emitters})
}
