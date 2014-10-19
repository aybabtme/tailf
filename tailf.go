/*
Package tailf implements an io.ReaderCloser to a file, which never reaches
io.EOF and instead, blocks until new data is appended to the file it
watches.  Effectively, the same as what `tail -f {{filename}}` does.

This works by putting an inotify watch on the file.

When the io.ReaderCloser is closed, the watch is cancelled and the
following reads will return normally until they reach the offset
that was last reported as the max file size, where the reader will
return EOF.
*/

package tailf

import (
	"fmt"
	"gopkg.in/fsnotify.v1"
	"io"
	"os"
	"sync"
)

type (
	// ErrFileTruncated signifies the underlying file of a tailf.Follower
	// has been truncated. The follower should be discarded.
	ErrFileTruncated struct{ error }
	// ErrFileRemoved signifies the underlying file of a tailf.Follower
	// has been removed. The follower should be discarded.
	ErrFileRemoved struct{ error }
)

type follower struct {
	filename string
	offset   int64
	maxSize  int64

	mu      sync.Mutex
	notifyc chan struct{}
	errc    chan error
	file    *os.File
	watch   *fsnotify.Watcher
}

// Follow returns an io.ReadCloser that follows the writes to a file.
func Follow(filename string, fromStart bool) (io.ReadCloser, error) {
	file, err := os.OpenFile(filename, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}

	var offset int64
	if !fromStart {
		n, err := file.Seek(0, os.SEEK_END)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		offset = n
	}

	fi, err := os.Stat(file.Name())
	if err != nil {
		return nil, err
	}

	watch, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	if err := watch.Add(file.Name()); err != nil {
		return nil, err
	}

	f := &follower{
		filename: filename,
		offset:   offset,
		maxSize:  fi.Size(),
		notifyc:  make(chan struct{}),
		errc:     make(chan error),
		file:     file,
		watch:    watch,
	}

	go f.followFile()

	return f, nil
}

// Close will remove the watch on the file. Subsequent reads to the file
// will eventually reach EOF.
func (f *follower) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	werr := f.watch.Close()
	cerr := f.file.Close()
	switch {
	case werr != nil && cerr == nil:
		return werr
	case werr == nil && cerr != nil:
		return cerr
	case werr != nil && cerr != nil:
		return fmt.Errorf("couldn't remove watch (%v) and close file (%v)", werr, cerr)
	}
	return nil
}

func (f *follower) Read(b []byte) (int, error) {
	f.mu.Lock()
	readable := f.maxSize - f.offset

	// check for errors before doing anything
	select {
	case err, open := <-f.errc:
		if !open && readable != 0 {
			break
		}
		f.mu.Unlock()
		if !open {
			return 0, io.EOF
		}
		return 0, err
	default:
	}

	if readable == 0 {
		f.mu.Unlock()

		// wait for the file to grow
		_, open := <-f.notifyc
		if !open {
			return 0, io.EOF
		}
		// then let the reader try again
		return 0, nil
	}

	n, err := f.file.Read(b[:readable])
	f.offset += int64(n)
	f.mu.Unlock()

	return n, err
}

func (f *follower) followFile() {
	defer f.watch.Close()
	defer close(f.notifyc)
	defer close(f.errc)
	// defer log.Printf("quitting the follow loop")
	for {
		select {
		case ev, open := <-f.watch.Events:
			if !open {
				return
			}
			err := f.handleFileEvent(ev)
			if err != nil {
				f.errc <- err
				return
			}
		case err, open := <-f.watch.Errors:
			if !open {
				return
			}
			if err != nil {
				f.errc <- err
				return
			}
		}

		select {
		case f.notifyc <- struct{}{}:
			// try to wake up whoever was waiting on an update
		default:
			// otherwise just wait for the next event
		}
	}
}

func (f *follower) handleFileEvent(ev fsnotify.Event) error {
	switch ev.Op {
	case fsnotify.Create:
		return ErrFileTruncated{
			fmt.Errorf("new file created with this name: %v", ev.String()),
		}
	case fsnotify.Remove:
		return ErrFileRemoved{
			fmt.Errorf("file was removed: %v", ev.String()),
		}
	case fsnotify.Rename:
		return f.reopenFile()
	case fsnotify.Write:
		return f.updateFile()
	default:
		panic(fmt.Sprintf("unknown event: %#v", ev))
	}
}

func (f *follower) reopenFile() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	fi, err := os.Stat(f.filename)
	if os.IsNotExist(err) {
		return ErrFileRemoved{fmt.Errorf("file was removed: %v", f.filename)}
	}
	if err != nil {
		return err
	}

	_ = f.file.Close()
	f.maxSize = fi.Size()
	f.file, err = os.OpenFile(f.filename, os.O_RDONLY, 0)
	if err != nil {
		return err
	}

	_, err = f.file.Seek(f.offset, os.SEEK_SET)
	return err
}

func (f *follower) updateFile() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	fi, err := os.Stat(f.filename)
	if err != nil {
		return err
	}
	f.maxSize = fi.Size()
	return nil
}
