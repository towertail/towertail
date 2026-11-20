package hub

import (
	"encoding/json"
	"sync"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/wire"
)

// sampleFrames holds the frames of one sample. The summary frames are
// built at ingest. The other frames are built when the first client needs
// them, and then kept.
type sampleFrames struct {
	nodeID uuid.UUID
	sample wire.Sample
	raw    json.RawMessage

	summary         []byte
	summaryWithHost []byte

	mu         sync.Mutex
	legacyData []byte
	details    map[wire.DetailTab][]byte
}

// newSampleFrames marshals the summary frames. The live frame has the
// host facts when factsChanged, and the health when healthChanged. The
// replay frame always has both.
func newSampleFrames(nodeID uuid.UUID, s wire.Sample, raw json.RawMessage, factsChanged, healthChanged bool) (*sampleFrames, error) {
	f := &sampleFrames{nodeID: nodeID, sample: s, raw: raw}
	full := s.Summary()
	facts := s.Facts()
	full.Host = &facts
	var err error
	if f.summaryWithHost, err = marshalSummary(nodeID, full); err != nil {
		return nil, err
	}
	if factsChanged && (healthChanged || full.Health == nil) {
		f.summary = f.summaryWithHost
		return f, nil
	}
	live := full
	if !factsChanged {
		live.Host = nil
	}
	if !healthChanged {
		live.Health = nil
	}
	f.summary, err = marshalSummary(nodeID, live)
	return f, err
}

func marshalSummary(nodeID uuid.UUID, sum wire.LiveSummary) ([]byte, error) {
	return json.Marshal(wire.WSMessage{Type: "summary", NodeID: &nodeID, Summary: &sum})
}

// legacy returns the sample frame of the legacy mode.
func (f *sampleFrames) legacy() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.legacyData == nil {
		f.legacyData, _ = json.Marshal(wire.WSMessage{Type: "sample", NodeID: &f.nodeID, Sample: f.raw})
	}
	return f.legacyData
}

func (f *sampleFrames) summaryFrame(withHost bool) []byte {
	if withHost {
		return f.summaryWithHost
	}
	return f.summary
}

// detailFrame returns false when the sample has no part for the tab.
func (f *sampleFrames) detailFrame(tab wire.DetailTab) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if data, ok := f.details[tab]; ok {
		return data, data != nil
	}
	if f.details == nil {
		f.details = map[wire.DetailTab][]byte{}
	}
	parts, ok := f.sample.Detail(tab)
	if !ok {
		f.details[tab] = nil
		return nil, false
	}
	body, err := json.Marshal(parts)
	if err != nil {
		return nil, false
	}
	data, err := json.Marshal(wire.WSMessage{Type: "detail", NodeID: &f.nodeID, Tab: tab, Detail: body})
	if err != nil {
		return nil, false
	}
	f.details[tab] = data
	return data, true
}
