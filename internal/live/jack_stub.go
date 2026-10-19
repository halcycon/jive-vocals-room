//go:build !jack

package live

func openJACK(InputOptions) (InputAnalyser, error) {
	return nil, ErrJACKDisabled
}

func JACKBuilt() bool { return false }
