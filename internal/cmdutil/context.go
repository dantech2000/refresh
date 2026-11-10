package cmdutil

import (
	"os"

	"github.com/dantech2000/refresh/internal/contexts"
)

// ActiveContext returns the active refresh context of the run: the one that
// REFRESH_CONTEXT names, or else the one that "refresh context use" saved.
// ok is false when no context is active. A REFRESH_CONTEXT that names no
// saved context, or a store that cannot be read, is an error.
func ActiveContext(f *Factory) (name string, c contexts.Context, ok bool, err error) {
	store, err := f.Contexts()
	if err != nil {
		return "", contexts.Context{}, false, err
	}
	file, err := store.Load()
	if err != nil {
		return "", contexts.Context{}, false, err
	}
	return file.Active(os.Getenv(contexts.EnvContext))
}
