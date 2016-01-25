package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sort"
	"syscall"
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

func trimb(p []byte) string {
	return string(bytes.Trim(p, "\x00"))
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
	return fmt.Sprintf("Host: %s Slot: %d State: %s File: %s Child: %d",
		trimb(s.Host[:]), s.Slot, states[s.State], trimb(s.File[:]), s.ClientPid)
}

func (s DistccState) FileStr() string {
	return trimb(s.File[:])
}

func (s DistccState) HostStr() string {
	return trimb(s.Host[:])
}

func (s DistccState) CheckKid() bool {
	// dunno how this happens, but it's not valid.
	if s.ClientPid == 0 {
		return false
	}

	kerr := syscall.Kill(int(s.ClientPid), syscall.Signal(0))
	if kerr == nil || kerr == syscall.EPERM {
		return true
	}

	return false
}

type MonitorState struct {
	DistccState

	StartTime time.Time
}

type Jobs map[string]MonitorState

type byHostSlot []MonitorState

func (s byHostSlot) Len() int      { return len(s) }
func (s byHostSlot) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s byHostSlot) Less(i, j int) bool {
	s1, s2 := s[i].HostStr(), s[j].HostStr()
	if s1 == s2 {
		return s[i].Slot < s[j].Slot
	}

	return s1 < s2
}

func (j Jobs) Sort() []MonitorState {
	s := make([]MonitorState, 0)

	for _, st := range j {
		s = append(s, st)
	}

	sort.Sort(byHostSlot(s))

	return s
}

func (j Jobs) Load(path string) error {
	buf, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}

	br := bytes.NewReader(buf)

	ds := DistccState{}

	err = binary.Read(br, binary.LittleEndian, &ds)
	if err != nil {
		return err
	}

	file := bytes.Trim(ds.File[:], "\x00")
	host := bytes.Trim(ds.Host[:], "\x00")

	copy(ds.File[:], file)
	copy(ds.Host[:], host)

	if len(file) == 0 || len(host) == 0 {
		return err
	}

	if !ds.CheckKid() {
		return fmt.Errorf("child %d is dead", ds.ClientPid)
	}

	if _, ok := j[path]; ok {
		// update
		ms := j[path]
		delete(j, path)
		ms.DistccState = ds
		j[path] = ms
		return nil
	}

	j[path] = MonitorState{
		DistccState: ds,
		StartTime:   time.Now().Round(1 * time.Second),
	}
	return nil
}

func (j Jobs) Remove(name string) {
	delete(j, name)
}

func str(x, y int, s string) {
	for _, c := range s {
		termbox.SetCell(x, y, c, termbox.ColorGreen, termbox.ColorDefault)
		x++
	}
}

func draw(jobs Jobs) {
	t := time.Now()
	termbox.Clear(termbox.ColorDefault, termbox.ColorDefault)

	j := jobs.Sort()

	str(1, 0, fmt.Sprintf("%d active jobs.", len(j)))
	str(0, 1, fmt.Sprintf("%-15s %-8s %-15s %-25s %-20s", "Host", "Slot", "State", "File", "Age"))

	i := 2
	for _, st := range j {
		str(0, i, fmt.Sprintf("%-15s %-8d %-15s %-25s %-20s", st.HostStr(), st.Slot, states[st.State], st.FileStr(), t.Round(1*time.Second).Sub(st.StartTime)))
		i++
	}

	termbox.Flush()
}
func main() {
	distcc := os.Getenv("DISTCC_DIR")
	if len(distcc) == 0 {
		distcc = os.Getenv("HOME") + "/.distcc"
	}

	distcc += "/state"

	// try to make it if it doesn't exist
	os.MkdirAll(distcc, 0775)

	watcher, err := inotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}

	err = watcher.Watch(distcc)
	if err != nil {
		log.Fatal(err)
	}

	jobs := Jobs{}

	// scan dir at startup
	files, _ := ioutil.ReadDir(distcc)
	for _, f := range files {
		fp := filepath.Join(distcc, f.Name())
		jobs.Load(fp)
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

mainloop:
	for {
		select {
		case <-timer:
			// check kids
			for n, j := range jobs {
				if !j.CheckKid() {
					delete(jobs, n)
				}
			}

			draw(jobs)
		case ev := <-event:
			switch ev.Type {
			case termbox.EventKey:
				switch ev.Ch {
				case rune(0):
					switch ev.Key {
					case termbox.KeyEsc, termbox.KeyCtrlC:
						break mainloop
					}
				case 'q':
					break mainloop
				}
			case termbox.EventError:
				panic(ev.Err)
			}
		case ev := <-watcher.Event:
			if ev.Mask&inotify.IN_MODIFY > 0 {
				jobs.Load(ev.Name)
			}

			if ev.Mask&inotify.IN_DELETE > 0 {
				jobs.Remove(ev.Name)
			}

		case err := <-watcher.Error:
			termbox.Close()
			log.Fatalf("watcher: %s", err)
		}
	}
}
