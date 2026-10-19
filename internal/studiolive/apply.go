package studiolive

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"
)

const (
	ProposalSchema = "jive-room-proposal/v1"
	ResultSchema   = "jive-room-apply-result/v1"
	ProfileSchema  = "jive-room-studiolive-profile/v1"

	SourceRingOut = "ringout"
	SourceProfile = "profile"

	addrProposalSet = "/dsp/proposal/set"
	addrApply       = "/dsp/apply"
	addrRevert      = "/dsp/revert"
	addrResult      = "/dsp/result"
)

var proposalIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// WireChange is one control target in raw desk units.
type WireChange struct {
	Address string `json:"address"`
	Raw     int    `json:"raw"`
}

// WireProposal is what the bridge validates, holds pending, and applies.
type WireProposal struct {
	Schema  string       `json:"schema_version"`
	ID      string       `json:"id"`
	Source  string       `json:"source"`
	Summary string       `json:"summary"`
	Changes []WireChange `json:"changes"`
}

// ApplyResult is the bridge's report for submit, apply, and revert.
type ApplyResult struct {
	Schema    string `json:"schema_version"`
	Action    string `json:"action"`
	ID        string `json:"id"`
	Source    string `json:"source,omitempty"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	Written   *int   `json:"written,omitempty"`
	Unchanged *int   `json:"unchanged,omitempty"`
	At        string `json:"at,omitempty"`
}

var ErrUnverifiedCalibration = errors.New("calibration for these controls has not been verified against the desk")

// Validate mirrors the bridge's structural checks so problems surface before
// sending. The bridge re-checks everything and is authoritative.
func (p WireProposal) Validate() error {
	if p.Schema != ProposalSchema {
		return fmt.Errorf("schema_version must be %s", ProposalSchema)
	}
	if !proposalIDPattern.MatchString(p.ID) {
		return fmt.Errorf("proposal id %q must be 1-64 characters of [A-Za-z0-9._-]", p.ID)
	}
	if p.Source != SourceRingOut && p.Source != SourceProfile {
		return fmt.Errorf("unknown proposal source %q", p.Source)
	}
	if len(p.Changes) == 0 || len(p.Changes) > 256 {
		return fmt.Errorf("a proposal needs 1-256 changes, has %d", len(p.Changes))
	}
	seen := map[string]bool{}
	for _, c := range p.Changes {
		ctl, ok := ParseAddress(c.Address)
		if !ok || !Proposable(ctl) {
			return fmt.Errorf("%s is not a proposable HPF/EQ/GEQ control", c.Address)
		}
		if seen[c.Address] {
			return fmt.Errorf("%s appears twice", c.Address)
		}
		seen[c.Address] = true
		if c.Raw < 0 || c.Raw > 255 || (KindOf(ctl) == KindSwitch && c.Raw > 1) {
			return fmt.Errorf("%s: raw %d out of range", c.Address, c.Raw)
		}
	}
	return nil
}

func newProposalID(prefix string, now time.Time) string {
	return fmt.Sprintf("%s-%s", prefix, now.UTC().Format("20060102T150405.000"))
}

// ProposalFromRingOut converts a ring-out proposal for the bridge. It refuses
// unmappable controls (GEQ without a dB calibration) and unverified
// calibration unless allowUnverified is set.
func ProposalFromRingOut(p Proposal, allowUnverified bool, now time.Time) (WireProposal, error) {
	if !p.Mappable {
		return WireProposal{}, fmt.Errorf("%s: no raw desk values (calibration missing)", p.Summary)
	}
	if !p.Verified && !allowUnverified {
		return WireProposal{}, fmt.Errorf("%s: %w", p.Summary, ErrUnverifiedCalibration)
	}
	w := WireProposal{Schema: ProposalSchema, ID: newProposalID("ringout", now), Source: SourceRingOut, Summary: p.Summary}
	// Cut the gain before retuning or enabling the band.
	order := map[string]int{"eqmidgain": 0, "eqmidfreq": 1, "eqmidhiq": 2, "eqmid": 3}
	cs := append([]ProposedControl(nil), p.Controls...)
	sort.SliceStable(cs, func(i, j int) bool { return order[cs[i].Control.Name] < order[cs[j].Control.Name] })
	for _, c := range cs {
		if c.Raw == nil {
			return WireProposal{}, fmt.Errorf("%s has no raw value", c.Control)
		}
		w.Changes = append(w.Changes, WireChange{Address: c.Control.Address(), Raw: int(*c.Raw)})
	}
	return w, w.Validate()
}

// ProfileControl is one saved desk value.
type ProfileControl struct {
	Address string   `json:"address"`
	Raw     int      `json:"raw"`
	Model   *float64 `json:"model_value,omitempty"`
	Unit    string   `json:"model_unit,omitempty"`
}

// Profile is a saved set of proposable desk controls, in raw units read from
// the desk itself, so pushing it needs no calibration.
type Profile struct {
	Schema    string           `json:"schema_version"`
	Label     string           `json:"label,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
	Controls  []ProfileControl `json:"controls"`
	Notes     []string         `json:"notes,omitempty"`
}

// ProfileFromState captures HPF/EQ for the given channels (all when empty)
// and, when includeGEQ, the 31 GEQ bands. Only proposable controls are saved.
func ProfileFromState(st *State, channels []int, includeGEQ bool, label string, now time.Time) (Profile, error) {
	if len(channels) == 0 {
		for n := 1; n <= InputChannels; n++ {
			channels = append(channels, n)
		}
	}
	p := Profile{
		Schema: ProfileSchema, Label: label, CreatedAt: now.UTC(),
		Notes: []string{"raw values are the desk's own; model values use the unverified session-label model and are informational only"},
	}
	var missing []string
	add := func(c Control) {
		v, ok := st.Get(c)
		if !ok {
			missing = append(missing, c.String())
			return
		}
		pc := ProfileControl{Address: c.Address(), Raw: int(v.Raw)}
		if cal, ok := CalibrationFor(c); ok {
			m := cal.ToUnit(v.Value)
			pc.Model, pc.Unit = &m, cal.Unit
		}
		p.Controls = append(p.Controls, pc)
	}
	names := make([]string, 0, len(proposable))
	for n := range proposable {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range channels {
		if _, ok := InputChannelNumber(InputChannelName(n)); !ok {
			return Profile{}, fmt.Errorf("channel %d out of range", n)
		}
		for _, name := range names {
			add(Control{Channel: InputChannelName(n), Name: name})
		}
	}
	if includeGEQ {
		for i := range GEQBands {
			c, _ := GEQBandControl(i)
			add(c)
		}
	}
	if len(missing) > 0 {
		return Profile{}, fmt.Errorf("mixer state incomplete; missing %d controls (first: %s)", len(missing), missing[0])
	}
	return p, nil
}

// ProfileDiff lists profile controls whose raw value differs from the desk.
func ProfileDiff(p Profile, st *State) ([]WireChange, error) {
	if p.Schema != ProfileSchema {
		return nil, fmt.Errorf("profile schema %q is not %s", p.Schema, ProfileSchema)
	}
	var out []WireChange
	for _, pc := range p.Controls {
		c, ok := ParseAddress(pc.Address)
		if !ok || !Proposable(c) {
			return nil, fmt.Errorf("profile contains non-proposable control %s", pc.Address)
		}
		v, ok := st.Get(c)
		if !ok {
			return nil, fmt.Errorf("desk state for %s unknown", pc.Address)
		}
		if int(v.Raw) != pc.Raw {
			out = append(out, WireChange{Address: pc.Address, Raw: pc.Raw})
		}
	}
	return out, nil
}

// ProposalFromProfile wraps the differing controls for the bridge.
func ProposalFromProfile(p Profile, changes []WireChange, now time.Time) (WireProposal, error) {
	summary := fmt.Sprintf("profile %q: %d controls", p.Label, len(changes))
	w := WireProposal{Schema: ProposalSchema, ID: newProposalID("profile", now), Source: SourceProfile, Summary: summary, Changes: changes}
	return w, w.Validate()
}

// SubmitProposal sends a proposal to the bridge, which validates it and holds
// it pending. Nothing is written until RequestApply.
func (c *BridgeClient) SubmitProposal(p WireProposal) error {
	if err := p.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return c.send(addrProposalSet, string(b))
}

// RequestApply asks the bridge to apply the pending proposal with this id.
func (c *BridgeClient) RequestApply(id string) error { return c.send(addrApply, id) }

// RequestRevert asks the bridge to undo its most recent apply.
func (c *BridgeClient) RequestRevert() error { return c.send(addrRevert) }
