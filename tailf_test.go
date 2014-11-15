package tailf_test

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aybabtme/tailf"
)

func TestImpl(t *testing.T) {
	var follower io.ReadCloser
	var err error
	withTempFile(t, func(t *testing.T, filename string, file *os.File) error {
		follower, err = tailf.Follow(filename, false)
		return err
	})
}

func TestCanFollowFile(t *testing.T) { withTempFile(t, canFollowFile) }

func canFollowFile(t *testing.T, filename string, file *os.File) error {

	toWrite := []string{
		"hello,",
		" world!",
	}

	want := strings.Join(toWrite, "")

	follow, err := tailf.Follow(filename, true)
	if err != nil {
		t.Fatalf("Failed creating tailf.follower: '%v'", err)
	}

	go func() {
		for _, str := range toWrite {
			t.Logf("Writing %d bytes", len(str))
			_, err := file.WriteString(str)
			if err != nil {
				t.Fatalf("Failed to write to file: '%v'", err)
			}
		}
	}()

	// this should work, without blocking forever
	data := make([]byte, len(want))
	_, err = io.ReadAtLeast(follow, data, len(want))
	if err != nil {
		return err
	}

	// this should block forever
	errc := make(chan error, 1)
	go func() {
		n, err := follow.Read(make([]byte, 1))
		t.Logf("Read %d bytes after closing", n)
		errc <- err
	}()

	if err := follow.Close(); err != nil {
		t.Errorf("Failed to close tailf.follower: %v", err)
	}

	got := string(data)
	if want != got {
		t.Errorf("Wanted '%v', got '%v'", want, got)
	}

	err = <-errc
	if err != io.EOF {
		t.Errorf("Expected EOF after closing the follower, got '%v' instead", err)
	}

	return nil
}

func TestCanFollowFileOverwritten(t *testing.T) { withTempFile(t, canFollowFileOverwritten) }

func canFollowFileOverwritten(t *testing.T, filename string, file *os.File) error {

	toWrite := []string{
		"hello,",
		" world!",
	}
	toWriteAgain := []string{
		"bonjour,",
		" le monde!",
	}

	want := strings.Join(append(toWrite, toWriteAgain...), "")

	follow, err := tailf.Follow(filename, true)
	if err != nil {
		return fmt.Errorf("creating follower: %v", err)
	}

	go func() {
		for _, str := range toWrite {
			t.Logf("Writing %d bytes", len(str))
			_, err := file.WriteString(str)
			if err != nil {
				t.Fatalf("failed to write to test file: %v", err)
			}
		}

		if err := os.Remove(filename); err != nil {
			t.Fatalf("couldn't delete file %q: %v", filename, err)
		}

		file, err = os.Create(filename)
		if err != nil {
			t.Fatalf("failed to write to test file: %v", err)
		}
		defer file.Close()
		for _, str := range toWriteAgain {
			t.Logf("Writing %d bytes", len(str))
			_, err := file.WriteString(str)
			if err != nil {
				t.Fatalf("failed to write to test file: %v", err)
			}
		}

	}()

	// this should work, without blocking forever
	data := make([]byte, len(want))
	_, err = io.ReadAtLeast(follow, data, len(want))
	if err != nil {
		return err
	}

	// this should block forever
	errc := make(chan error, 1)
	go func() {
		n, err := follow.Read(make([]byte, 1))
		t.Logf("read %d bytes after closing", n)
		errc <- err
	}()

	if err := follow.Close(); err != nil {
		t.Errorf("failed to close follower: %v", err)
	}

	got := string(data)
	if want != got {
		t.Errorf("want %v, got %v", want, got)
	}

	err = <-errc
	if err != io.EOF {
		t.Errorf("expected EOF after closing follower, got %v", err)
	}

	return nil
}

func TestCanFollowFileFromEnd(t *testing.T) { withTempFile(t, canFollowFileFromEnd) }

func canFollowFileFromEnd(t *testing.T, filename string, file *os.File) error {

	_, err := file.WriteString("shouldn't read this part")
	if err != nil {
		return err
	}

	toWrite := []string{
		"hello,",
		" world!",
	}

	want := strings.Join(toWrite, "")

	follow, err := tailf.Follow(filename, false)
	if err != nil {
		t.Fatalf("Failed creating tailf.follower: %v", err)
	}

	go func() {
		for _, str := range toWrite {
			t.Logf("Writing %d bytes", len(str))
			_, err := file.WriteString(str)
			if err != nil {
				t.Fatalf("Failed to write to file: '%v'", err)
			}
		}
	}()

	// this should work, without blocking forever
	data := make([]byte, len(want))
	_, err = io.ReadAtLeast(follow, data, len(want))
	if err != nil {
		return err
	}

	// this should block forever
	errc := make(chan error, 1)
	go func() {
		n, err := io.ReadAtLeast(follow, make([]byte, 1), 1)
		t.Logf("Read %d bytes after closing", n)
		errc <- err
	}()

	if err := follow.Close(); err != nil {
		t.Errorf("Failed to close tailf.follower: %v", err)
	}

	got := string(data)
	if want != got {
		t.Errorf("Wanted '%v', got '%v'", want, got)
	}

	err = <-errc
	if err != io.EOF {
		t.Errorf("Expected EOF after closing the follower, got '%v' instead", err)
	}

	return nil
}

func TestFollowTruncation(t *testing.T) { withTempFile(t, canFollowTruncation) }

func canFollowTruncation(t *testing.T, filename string, file *os.File) error {
	follow, err := tailf.Follow(filename, false)
	if err != nil {
		t.Fatalf("failed to create follower: %v", err)
	}

	for i := int64(0); i < 10; i++ {
		if i%2 == 0 {
			t.Logf("Truncating the file")
			file, err := os.OpenFile(filename, os.O_TRUNC, os.ModeTemporary)
			if err != nil {
				t.Fatalf("Unable to truncate file: %v", err)
			}
			file.Close()
		}

		wantBuf := strconv.AppendInt(make([]byte, 0), i, 10)
		_, err = file.WriteString(string(wantBuf))
		if err != nil {
			t.Errorf("Write failed, %v", err)
		}

		gotBuf := make([]byte, 1)
		_, err := follow.Read(gotBuf)
		if err != nil {
			t.Fatalf("Failed to read: %v", err)
		}

		if !bytes.Equal(gotBuf, wantBuf) {
			t.Logf("Want=%x", wantBuf)
			t.Logf(" got=%x", gotBuf)
			t.Errorf("Missed write after truncation")
		}
	}

	if err := follow.Close(); err != nil {
		t.Errorf("Failed to close follower: %v", err)
	}

	return nil
}

// Continually read from a file that is having data written to it every 5ms, and randomly truncated every [5,55]ms
func TestFollowRandomTruncation(t *testing.T) {
	withTempFile(t, func(t *testing.T, filename string, file *os.File) error {
		follow, err := tailf.Follow(filename, false)
		if err != nil {
			t.Fatalf("Failed creating tailf.follower: %v", err)
		}

		expected := "Bytes!"

		writer := time.NewTicker(time.Millisecond * 5)
		defer writer.Stop()

		go func() {
			for _ = range writer.C {
				t.Logf("Writing: '%v'", expected)
				file.WriteString(expected + "\n")
			}
		}()

		go func() {
			for {
				time.Sleep(time.Duration(time.Millisecond) * time.Duration(rand.Intn(50)+5))

				t.Log("Truncating the file")
				trunc, err := os.OpenFile(filename, os.O_TRUNC, os.ModeTemporary)
				if err != nil {
					t.Fatalf("Unable to truncate file")
				}
				trunc.Close()
			}
		}()

		go func() {
			scanner := bufio.NewScanner(follow)
			for scanner.Scan() {
				t.Log("Read:", scanner.Text())
				if actual := strings.Trim(scanner.Text(), "\x00"); actual != expected {
					t.Errorf("Bad read! Expected(%v) != Actual(%v)", []byte(expected), []byte(actual))
				}
			}

			if err := scanner.Err(); err != nil && err != io.EOF {
				t.Error("Scanner returned an error", err)
			}
		}()

		time.Sleep(time.Duration(time.Millisecond * 100))
		follow.Close()

		return nil
	})
}

// Run for 50ms constantly trying to read from a tailf.follower that has nothing to read
func TestSpinningReader(t *testing.T) {
	withTempFile(t, func(t *testing.T, filename string, file *os.File) error {
		follow, err := tailf.Follow(filename, false)
		if err != nil {
			t.Fatalf("Failed creating tailf.follower: %v", err)
		}

		stop := make(chan struct{})
		timeout := time.AfterFunc(time.Duration(time.Millisecond*50), func() { stop <- struct{}{} })

		read := make(chan struct{}, 1000)
		go func() {
			t.Log("Reader running")
			buf := make([]byte, 100)
			for {
				_, err := follow.Read(buf)
				if err != nil {
					t.Errorf("Read error: %v", err)
				}
				t.Log("Read completed")
				read <- struct{}{}
			}
		}()

		count := 0

		func() {
			for {
				select {
				case <-stop:
					return
				case <-read:
					count += 1
					if count > 5 {
						t.Error("Spinning on read")
					}
				}
			}
		}()

		t.Logf("Read ran '%v' times", count)
		timeout.Stop()
		return nil
	})
}

func withTempFile(t *testing.T, action func(t *testing.T, filename string, file *os.File) error) {
	dir, err := ioutil.TempDir(os.TempDir(), "tailf_test_dir")
	if err != nil {
		t.Fatalf("Unable to create temp dir: '%v'", err)
	}

	file, err := ioutil.TempFile(dir, "tailf_test")
	if err != nil {
		t.Fatalf("Unable to create temp file: %v", err)
	}
	defer os.RemoveAll(dir)
	defer file.Close()

	errc := make(chan error)
	go func() { errc <- action(t, file.Name(), file) }()

	select {
	case err = <-errc:
		if err != nil {
			t.Errorf("failure: %v", err)
		}
	case <-time.After(time.Millisecond * 150):
		t.Fatal("Test took too long :(")
	}
}
