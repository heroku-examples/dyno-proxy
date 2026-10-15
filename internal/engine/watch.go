package engine

import (
	"context"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watchDebounce coalesces rapid file events into a single regenerate.
const watchDebounce = 500 * time.Millisecond

// watch observes projectPath for write/rename events and invokes onChange
// (debounced) so the engine can re-render and ask the supervisor to reload. It
// returns when ctx is cancelled or the watcher closes. Replaces the legacy
// watchManifestAndRegenerate; onChange
// carries all the regenerate/reload logic.
func watch(ctx context.Context, projectPath string, logf func(level, msg string), onChange func()) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logf("error", "failed to create file watcher: "+err.Error())
		return
	}
	defer watcher.Close()

	if err := watcher.Add(projectPath); err != nil {
		logf("error", "failed to watch project.toml: "+err.Error())
		return
	}
	logf("info", "watching project.toml for changes: "+projectPath)

	var debounce *time.Timer
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Rename) == 0 {
				continue
			}
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(watchDebounce, onChange)
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			logf("error", "file watcher error: "+err.Error())
		}
	}
}
