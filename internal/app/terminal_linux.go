//go:build linux

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func (a *App) hiddenSecret(ctx context.Context, file *os.File) (value string, retErr error) {
	fd := int(file.Fd())
	state, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return "", invalid("secret prompt requires a terminal; use --stdin")
	}
	hidden := *state
	hidden.Lflag &^= unix.ECHO
	if err = unix.IoctlSetTermios(fd, unix.TCSETS, &hidden); err != nil {
		return "", err
	}
	defer func() { retErr = errors.Join(retErr, unix.IoctlSetTermios(fd, unix.TCSETS, state)) }()
	if _, err = fmt.Fprint(a.Err, "API key: "); err != nil {
		return "", err
	}
	line, err := readLine(ctx, file)
	if err != nil {
		return "", err
	}
	if _, err = fmt.Fprintln(a.Err); err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
