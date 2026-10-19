//go:build jack || pipewire

package live

func (o InputOptions) ringFrames() int {
	if o.RingFrames > 0 {
		return o.RingFrames
	}
	return DefaultRingFrames
}
