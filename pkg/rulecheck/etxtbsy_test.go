package rulecheck

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStartCmdParallelWrites writes executables and starts them from many
// goroutines at once, the pattern that yields "text file busy" when a fork
// races an open write fd. startCmd must absorb it.
func TestStartCmdParallelWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("stress loop")
	}

	if _, err := os.Stat("/bin/true"); err != nil {
		t.Skip("no /bin/true")
	}

	script := []byte("#!/bin/sh\nexit 0\n")
	dir := t.TempDir()

	var wg sync.WaitGroup

	errs := make(chan error, 16*10)

	for i := range 16 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for j := range 10 {
				path := filepath.Join(dir, "tool-"+string(rune('a'+i))+string(rune('0'+j)))

				tmp, err := os.CreateTemp(dir, "w-*.tmp")
				if err != nil {
					errs <- err

					return
				}

				_, werr := tmp.Write(script)
				serr := tmp.Sync()
				cerr := tmp.Close()

				if err := errors.Join(werr, serr, cerr, os.Chmod(tmp.Name(), 0o755)); err != nil {
					errs <- err

					return
				}

				if err := os.Rename(tmp.Name(), path); err != nil {
					errs <- err

					return
				}

				cmd, stderr, err := startCmd(context.Background(), path)
				if err != nil {
					errs <- err

					return
				}

				_ = stderr.Close()
				errs <- cmd.Wait()
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
}
