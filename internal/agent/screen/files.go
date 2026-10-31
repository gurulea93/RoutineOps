//go:build screen

package screen

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/Floodww/RoutineOps/proto"
)

// Файловый менеджер сеанса с управлением (как в RustDesk): просмотр папок, загрузка на
// машину сотрудника и скачивание с неё. Только в сеансе с управлением — оператор и так
// действует на этой машине от имени сотрудника; каждый перенос сервер пишет в аудит.
//
// Транспорт — тот же стрим сеанса, без изменений proto: запросы приходят сигналом
// SessionSignal.ice с sdp_mid="fs" (JSON в candidate), ответы уходят так же. Данные — куски
// по fsChunk байт (base64 в JSON), с окном подтверждений, чтобы скачивание не забивало
// очереди и не душило картинку.

// FilesMid — метка сигнала файлового менеджера (SessionSignal.ice.sdp_mid).
const FilesMid = "fs"

const (
	fsChunk  = 96 << 10 // в base64 ~128 КБ: с запасом меньше maxWireMsg
	fsWindow = 16       // кусков скачивания в полёте без подтверждения
	fsIdle   = 2 * time.Minute
)

type fsReq struct {
	ID   string `json:"id"`
	Op   string `json:"op"` // home | list | mkdir | read | ack | cancel | write
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"` // write: имя файла в папке Path
	Off  int64  `json:"off,omitempty"`
	Data []byte `json:"data,omitempty"`
	EOF  bool   `json:"eof,omitempty"`
}

type fsEntry struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // Unix, секунды
}

type fsResp struct {
	ID      string    `json:"id"`
	OK      bool      `json:"ok"`
	Err     string    `json:"err,omitempty"`
	Path    string    `json:"path,omitempty"`
	Entries []fsEntry `json:"entries,omitempty"`
	Size    int64     `json:"size,omitempty"`
	Off     int64     `json:"off,omitempty"`
	Data    []byte    `json:"data,omitempty"`
	EOF     bool      `json:"eof,omitempty"`
}

type fsUpload struct {
	f         *os.File
	tmp, dest string
	off       int64
	last      time.Time
}

type fsDownload struct {
	credit chan struct{}
	stop   chan struct{}
	once   sync.Once
}

func (d *fsDownload) cancel() { d.once.Do(func() { close(d.stop) }) }

type fileServer struct {
	send func([]byte) error
	in   chan fsReq
	done chan struct{}

	mu    sync.Mutex
	ups   map[string]*fsUpload
	downs map[string]*fsDownload
}

func newFileServer(send func([]byte) error) *fileServer {
	fs := &fileServer{
		send: send, in: make(chan fsReq, 256), done: make(chan struct{}),
		ups: map[string]*fsUpload{}, downs: map[string]*fsDownload{},
	}
	go fs.loop()
	return fs
}

// handleSignal — сигнал от службы; false — сигнал не файловый.
func (fs *fileServer) handleSignal(sig *pb.SessionSignal) bool {
	ice := sig.GetIce()
	if ice == nil || ice.GetSdpMid() != FilesMid {
		return false
	}
	var r fsReq
	if json.Unmarshal([]byte(ice.GetCandidate()), &r) != nil || r.ID == "" {
		return true
	}
	select {
	case fs.in <- r:
	case <-fs.done:
	}
	return true
}

func (fs *fileServer) close() {
	select {
	case <-fs.done:
		return
	default:
	}
	close(fs.done)
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for id, u := range fs.ups {
		_ = u.f.Close()
		_ = os.Remove(u.tmp) // недокачанное не оставляем
		delete(fs.ups, id)
	}
	for id, d := range fs.downs {
		d.cancel()
		delete(fs.downs, id)
	}
}

func (fs *fileServer) reply(r fsResp) {
	j, err := json.Marshal(r)
	if err != nil {
		return
	}
	b, err := proto.Marshal(&pb.SessionSignal{Msg: &pb.SessionSignal_Ice{Ice: &pb.IceCandidate{
		Candidate: string(j), SdpMid: FilesMid,
	}}})
	if err != nil || len(b) > maxWireMsg {
		return
	}
	_ = fs.send(b)
}

func (fs *fileServer) fail(id string, err error) { fs.reply(fsResp{ID: id, Err: err.Error()}) }

func (fs *fileServer) loop() {
	sweep := time.NewTicker(30 * time.Second)
	defer sweep.Stop()
	for {
		select {
		case <-fs.done:
			return
		case <-sweep.C:
			fs.sweepIdle()
		case r := <-fs.in:
			fs.handle(r)
		}
	}
}

func (fs *fileServer) handle(r fsReq) {
	switch r.Op {
	case "home":
		fs.reply(fsResp{ID: r.ID, OK: true, Path: userHomeDir()})
	case "list":
		entries, err := listDir(r.Path)
		if err != nil {
			fs.fail(r.ID, err)
			return
		}
		fs.reply(fsResp{ID: r.ID, OK: true, Path: r.Path, Entries: entries})
	case "mkdir":
		if r.Path == "" || r.Name == "" {
			fs.fail(r.ID, errors.New("path and name required"))
			return
		}
		p := filepath.Join(r.Path, cleanName(r.Name))
		if err := os.Mkdir(p, 0o755); err != nil {
			fs.fail(r.ID, err)
			return
		}
		fs.reply(fsResp{ID: r.ID, OK: true, Path: p})
	case "read":
		fs.startRead(r)
	case "ack":
		fs.mu.Lock()
		d := fs.downs[r.ID]
		fs.mu.Unlock()
		if d != nil {
			select {
			case d.credit <- struct{}{}:
			default:
			}
		}
	case "cancel":
		fs.mu.Lock()
		d := fs.downs[r.ID]
		delete(fs.downs, r.ID)
		u := fs.ups[r.ID]
		delete(fs.ups, r.ID)
		fs.mu.Unlock()
		if d != nil {
			d.cancel()
		}
		if u != nil {
			_ = u.f.Close()
			_ = os.Remove(u.tmp)
		}
	case "write":
		fs.write(r)
	default:
		fs.fail(r.ID, errors.New("unknown op "+r.Op))
	}
}

// cleanName — только имя, без каталогов: «..\..\Windows\x» превращается в «x».
func cleanName(n string) string {
	n = strings.ReplaceAll(n, "\\", "/")
	n = filepath.Base(filepath.Clean("/" + n))
	if n == "." || n == "/" || n == "" {
		return "file"
	}
	return n
}

func (fs *fileServer) write(r fsReq) {
	fs.mu.Lock()
	u := fs.ups[r.ID]
	fs.mu.Unlock()
	if u == nil {
		if r.Off != 0 || r.Path == "" || r.Name == "" {
			fs.fail(r.ID, errors.New("upload must start at offset 0 with path and name"))
			return
		}
		dest := filepath.Join(r.Path, cleanName(r.Name))
		tmp := dest + ".rops-part"
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			fs.fail(r.ID, err)
			return
		}
		u = &fsUpload{f: f, tmp: tmp, dest: dest}
		fs.mu.Lock()
		fs.ups[r.ID] = u
		fs.mu.Unlock()
	}
	u.last = time.Now()
	if r.Off != u.off {
		fs.abortUpload(r.ID, u, errors.New("out-of-order chunk"))
		return
	}
	if len(r.Data) > 0 {
		if _, err := u.f.Write(r.Data); err != nil {
			fs.abortUpload(r.ID, u, err)
			return
		}
		u.off += int64(len(r.Data))
	}
	if !r.EOF {
		fs.reply(fsResp{ID: r.ID, OK: true, Off: u.off})
		return
	}
	fs.mu.Lock()
	delete(fs.ups, r.ID)
	fs.mu.Unlock()
	if err := u.f.Close(); err != nil {
		_ = os.Remove(u.tmp)
		fs.fail(r.ID, err)
		return
	}
	if err := os.Rename(u.tmp, u.dest); err != nil {
		_ = os.Remove(u.tmp)
		fs.fail(r.ID, err)
		return
	}
	fs.reply(fsResp{ID: r.ID, OK: true, Off: u.off, EOF: true, Path: u.dest})
}

func (fs *fileServer) abortUpload(id string, u *fsUpload, err error) {
	fs.mu.Lock()
	delete(fs.ups, id)
	fs.mu.Unlock()
	_ = u.f.Close()
	_ = os.Remove(u.tmp)
	fs.fail(id, err)
}

// sweepIdle — брошенные загрузки (оператор закрыл вкладку) не держат файл открытым.
func (fs *fileServer) sweepIdle() {
	fs.mu.Lock()
	var stale []string
	for id, u := range fs.ups {
		if time.Since(u.last) > fsIdle {
			stale = append(stale, id)
		}
	}
	fs.mu.Unlock()
	for _, id := range stale {
		fs.handle(fsReq{ID: id, Op: "cancel"})
	}
}

func (fs *fileServer) startRead(r fsReq) {
	f, err := os.Open(r.Path)
	if err != nil {
		fs.fail(r.ID, err)
		return
	}
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		_ = f.Close()
		if err == nil {
			err = errors.New("is a directory")
		}
		fs.fail(r.ID, err)
		return
	}
	d := &fsDownload{credit: make(chan struct{}, fsWindow*2), stop: make(chan struct{})}
	for i := 0; i < fsWindow; i++ {
		d.credit <- struct{}{}
	}
	fs.mu.Lock()
	fs.downs[r.ID] = d
	fs.mu.Unlock()
	go func() {
		defer f.Close()
		defer func() {
			fs.mu.Lock()
			if fs.downs[r.ID] == d {
				delete(fs.downs, r.ID)
			}
			fs.mu.Unlock()
		}()
		buf := make([]byte, fsChunk)
		var off int64
		first := true
		for {
			select {
			case <-d.credit:
			case <-d.stop:
				return
			case <-fs.done:
				return
			case <-time.After(fsIdle):
				return // сервер перестал подтверждать — бросаем
			}
			n, rerr := io.ReadFull(f, buf)
			eof := errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF)
			if rerr != nil && !eof {
				fs.fail(r.ID, rerr)
				return
			}
			resp := fsResp{ID: r.ID, OK: true, Off: off, Data: buf[:n], EOF: eof}
			if first {
				resp.Size, resp.Path, first = st.Size(), r.Path, false
			}
			fs.reply(resp)
			off += int64(n)
			if eof {
				return
			}
		}
	}()
}

func listDir(p string) ([]fsEntry, error) {
	if p == "" {
		return rootEntries(), nil
	}
	des, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	out := make([]fsEntry, 0, len(des))
	for _, de := range des {
		e := fsEntry{Name: de.Name(), Dir: de.IsDir()}
		if info, err := de.Info(); err == nil {
			e.MTime = info.ModTime().Unix()
			if !e.Dir {
				e.Size = info.Size()
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	if len(out) > 5000 {
		out = out[:5000] // ответ должен влезть в одно сообщение
	}
	return out, nil
}
