//go:build !pipewire

package live

func OpenInputAnalyser() (InputAnalyser, error) {
	return nil, ErrPipeWireDisabled
}

func PipeWireBuilt() bool { return false }
