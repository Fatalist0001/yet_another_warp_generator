//go:build windows

package cli

import (
	"errors"
	"os"
)

func receiveTunFD(string) (*os.File, error) {
	return nil, errors.New("cli: передача fd TUN через unix-сокет недоступна на Windows")
}
