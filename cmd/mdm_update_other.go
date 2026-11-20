//go:build !darwin

package cmd

import (
	"context"
	"errors"
	"io"
)

func runSystemMDMUpdate(context.Context, io.Writer, bool, string) error {
	return errors.New("managed Doomer updates require an MDM-enrolled Mac")
}
