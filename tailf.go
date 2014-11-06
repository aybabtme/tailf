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
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"sync"

	"gopkg.in/fsnotify.v1"
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

	mu             sync.Mutex
	notifyc        chan struct{}
	errc           chan error
	file           *os.File
	fileReader     *bufio.Reader
	rotationBuffer *bytes.Buffer
	reader         io.Reader
	watch          *fsnotify.Watcher
}

// Follow returns an io.ReadCloser that follows the writes to a file.
func Follow(filename string, fromStart bool) (io.ReadCloser, error) {
	file, err := os.OpenFile(filename, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}

	if !fromStart {
		_, err := file.Seek(0, os.SEEK_END)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
	}

	reader := bufio.NewReader(file)

	watch, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	if err := watch.Add(file.Name()); err != nil {
		return nil, err
	}

	f := &follower{
		filename:   filename,
		notifyc:    make(chan struct{}),
		errc:       make(chan error),
		file:       file,
		fileReader: reader,
		reader:     reader,
		watch:      watch,
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

	// Refill the buffer
	_, err := f.fileReader.Peek(1)
	if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
		return 0, err
	}
	readable := f.fileReader.Buffered()

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

	n, err := f.reader.Read(b[:imin(readable, len(b))])
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
	if ev.Op&fsnotify.Create == fsnotify.Create {
		return ErrFileTruncated{
			fmt.Errorf("new file created with this name: %v", ev.String()),
		}
	} else if ev.Op&fsnotify.Remove == fsnotify.Remove {
		return ErrFileRemoved{
			fmt.Errorf("file was removed: %v", ev.String()),
		}
	} else if ev.Op&fsnotify.Rename == fsnotify.Rename {
		return f.reopenFile()
	} else if ev.Op&fsnotify.Write == fsnotify.Write {
		return f.updateFile()
	} else if ev.Op&fsnotify.Chmod == fsnotify.Chmod {
		// drop it
		return nil
	}

	panic(fmt.Sprintf("unknown event: %#v", ev))
}

func (f *follower) reopenFile() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	_, err := os.Stat(f.filename)
	if os.IsNotExist(err) {
		return ErrFileRemoved{fmt.Errorf("file was removed: %v", f.filename)}
	}
	if err != nil {
		return err
	}

	if err := f.file.Close(); err != nil {
		return err
	}

	f.file, err = os.OpenFile(f.filename, os.O_RDONLY, 0)
	if err != nil {
		return err
	}

	buf := bytes.NewBuffer(make([]byte, 0, f.fileReader.Buffered()))
	if n, err := io.Copy(buf, f.fileReader); err != nil && n != int64(f.fileReader.Buffered()) {
		return err
	}

	f.fileReader = bufio.NewReader(f.file)

	f.reader = io.MultiReader(buf, f.fileReader)

	return err
}

func (f *follower) updateFile() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	_, err := f.fileReader.Peek(1) // Refill the buffer
	if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
		return err
	}

	return nil
}

func imin(a, b int) int {
	if a < b {
		return a
	}
	return b
}
