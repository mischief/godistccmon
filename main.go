package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"sort"
	"time"

	"github.com/nsf/termbox-go"
	"golang.org/x/exp/inotify"
)

var states = []string{
	"Startup",
	"Blocked",
	"Connecting",
	"Preprocessing",
	"Sending",
	"Compiling",
	"Receiving",
	"Finished",
}

type DistccState struct {
	StructSize int64
	Magic      uint64
	ClientPid  uint64
	File       [128]byte
	Host       [128]byte
	Slot       uint32
	State      uint64
}

func (s DistccState) String() string {
	return fmt.Sprintf("Host: %s Slot: %d State: %s File: %s",
		bytes.Trim(s.Host[:], "\x00"), s.Slot, states[s.State], bytes.Trim(s.File[:], "\x00"))
}

type Jobs map[string]DistccState

type byHostSlot []DistccState

func (s byHostSlot) Len() int      { return len(s) }
func (s byHostSlot) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s byHostSlot) Less(i, j int) bool {
	if s[i].Host == s[j].Host {
		return s[i].Slot < s[j].Slot
	}

	return true
}

func (j Jobs) Sort() []DistccState {
	s := make([]DistccState, 0)

	for _, st := range j {
		s = append(s, st)
	}

	sort.Sort(byHostSlot(s))

	return s
}

func str(x, y int, s string) {
	for _, c := range s {
		termbox.SetCell(x, y, c, termbox.ColorGreen, termbox.ColorDefault)
		x++
	}
}

func main() {
	distcc := os.Getenv("DISTCC_DIR")
	if len(distcc) == 0 {
		distcc = os.Getenv("HOME") + "/.distcc"
	}

	distcc += "/state"

	watcher, err := inotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}

	err = watcher.Watch(distcc)
	if err != nil {
		log.Fatal(err)
	}

	event := make(chan termbox.Event)

	err = termbox.Init()
	if err != nil {
		log.Fatal(err)
	}

	defer termbox.Close()

	go func() {
		for {
			event <- termbox.PollEvent()
		}
	}()

	timer := time.Tick(500 * time.Millisecond)

	jobs := Jobs{}

	draw := func() {
		termbox.Clear(termbox.ColorDefault, termbox.ColorDefault)

		j := jobs.Sort()

		str(1, 0, fmt.Sprintf("%d active jobs.", len(j)))

		i := 1
		for _, st := range j {
			str(0, i, fmt.Sprintf("%s", st))
			i++
		}

		termbox.Flush()
	}

mainloop:
	for {
		select {
		case <-timer:
			draw()
		case ev := <-event:
			switch ev.Type {
			case termbox.EventKey:
				switch ev.Key {
				case termbox.KeyEsc:
					break mainloop
				}
			case termbox.EventError:
				panic(ev.Err)
			}
		case ev := <-watcher.Event:
			if ev.Mask&inotify.IN_MODIFY > 0 {
				buf, _ := ioutil.ReadFile(ev.Name)

				br := bytes.NewReader(buf)

				ds := DistccState{}

				err := binary.Read(br, binary.LittleEndian, &ds)
				if err != nil {
					break
				}

				file := bytes.Trim(ds.File[:], "\x00")
				host := bytes.Trim(ds.Host[:], "\x00")

				copy(ds.File[:], file)
				copy(ds.Host[:], host)

				if len(file) == 0 || len(host) == 0 {
					break
				}

				jobs[ev.Name] = ds
			}

			if ev.Mask&inotify.IN_DELETE > 0 {
				delete(jobs, ev.Name)
			}

		case err := <-watcher.Error:
			termbox.Close()
			log.Fatalf("watcher: %s", err)
		}
	}
}
