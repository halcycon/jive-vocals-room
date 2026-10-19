//go:build !pipewire

package live

func openPipeWire() (InputAnalyser, error) {
	return nil, ErrPipeWireDisabled
}

func PipeWireBuilt() bool { return false }
