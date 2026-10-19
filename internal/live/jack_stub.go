//go:build !jack

package live

func openJACK(InputOptions) (InputAnalyser, error) {
	return nil, ErrJACKDisabled
}

func JACKSampleRate(string) (int, error) { return 0, ErrJACKDisabled }

func JACKBuilt() bool { return false }
