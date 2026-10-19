//go:build !pipewire

package live

func openPipeWire(InputOptions) (InputAnalyser, error) {
	return nil, ErrPipeWireDisabled
}

func PipeWireBuilt() bool { return false }
