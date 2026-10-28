package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/atfa/duo/internal/remote"
	"github.com/atfa/duo/internal/xduo"
	"github.com/gdamore/tcell/v3"
)

const manager = "xduo-manager"

type instance struct {
	ID        string    `json:"id"`
	Project   string    `json:"project"`
	Tmux      string    `json:"tmux"`
	Status    string    `json:"status_file"`
	Pairing   string    `json:"pairing_file"`
	Created   time.Time `json:"created"`
	Activated time.Time `json:"activated"`
}
type registry struct {
	Items []instance `json:"instances"`
}

func tmux(args ...string) error {
	b, e := exec.Command("tmux", args...).CombinedOutput()
	if e != nil {
		return fmt.Errorf("tmux: %s: %w", strings.TrimSpace(string(b)), e)
	}
	return nil
}
func alive(name string) bool { return exec.Command("tmux", "has-session", "-t", "="+name).Run() == nil }
func save(path string, r registry) error {
	b, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".instances-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), path)
}
func load(path string) (registry, error) {
	var r registry
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return r, nil
	}
	if e != nil {
		return r, e
	}
	e = json.Unmarshal(b, &r)
	return r, e
}
func readStatus(i instance) (xduo.Status, bool) {
	var s xduo.Status
	b, e := os.ReadFile(i.Status)
	if e != nil {
		return s, false
	}
	e = json.Unmarshal(b, &s)
	return s, e == nil
}
func taskState(s xduo.Status, valid, live bool, now time.Time) string {
	if !live {
		return "stop"
	}
	if !valid || !s.Running || now.Sub(s.Updated) > 5*time.Second || s.Updated.After(now.Add(5*time.Second)) {
		return "Unknown"
	}
	if s.Phase == "DONE" {
		return "stop"
	}
	if s.Pause == "paused" || s.Pause == "pausing" {
		return "pause"
	}
	if !s.Started {
		return "Starting"
	}
	return "playing"
}
func create(root, duo, path string) (instance, error) {
	var i instance
	p, e := filepath.Abs(strings.TrimSpace(path))
	if e != nil {
		return i, e
	}
	p, e = filepath.EvalSymlinks(p)
	if e != nil {
		return i, e
	}
	st, e := os.Stat(p)
	if e != nil || !st.IsDir() {
		return i, fmt.Errorf("项目目录无效: %s", p)
	}
	c := exec.Command("git", "-C", p, "rev-parse", "--is-inside-work-tree")
	out, e := c.Output()
	if e != nil || strings.TrimSpace(string(out)) != "true" {
		return i, fmt.Errorf("不是 Git 工作目录: %s", p)
	}
	exe, e := exec.LookPath(duo)
	if e != nil {
		return i, fmt.Errorf("找不到 duo: %w", e)
	}
	exe, e = filepath.Abs(exe)
	if e != nil {
		return i, e
	}
	var b [12]byte
	if _, e = rand.Read(b[:]); e != nil {
		return i, e
	}
	id := hex.EncodeToString(b[:])
	dir := filepath.Join(root, id)
	if e = os.MkdirAll(dir, 0700); e != nil {
		return i, e
	}
	i = instance{ID: id, Project: p, Tmux: "xduo-" + id, Status: filepath.Join(dir, "status.json"), Pairing: filepath.Join(dir, "remote.json"), Created: time.Now().UTC()}
	e = tmux("new-session", "-d", "-s", i.Tmux, "-c", p, "-e", "XDUO_STATUS_FILE="+i.Status, "-e", "DUO_REMOTE_PAIRING_FILE="+i.Pairing, exe, p)
	if e != nil {
		os.RemoveAll(dir)
		return instance{}, e
	}
	return i, nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "xduo:", err)
		os.Exit(1)
	}
}
func run() error {
	panel := flag.Bool("panel", false, "internal manager panel")
	duo := flag.String("duo", "", "path to duo executable")
	flag.Parse()
	if _, e := exec.LookPath("tmux"); e != nil {
		return errors.New("需要安装 tmux")
	}
	if *duo == "" {
		*duo = os.Getenv("XDUO_DUO_PATH")
	}
	if *duo == "" {
		*duo = "duo"
	}
	home, e := os.UserHomeDir()
	if e != nil {
		return e
	}
	root := filepath.Join(home, ".duo", "xduo")
	reg := filepath.Join(root, "instances.json")
	if !*panel {
		if !alive(manager) {
			exe, e := os.Executable()
			if e != nil {
				return e
			}
			if e = tmux("new-session", "-d", "-s", manager, exe, "--panel", "--duo", *duo); e != nil {
				return e
			}
		}
		args := []string{"attach-session", "-t", "=" + manager}
		if os.Getenv("TMUX") != "" {
			args = []string{"switch-client", "-t", "=" + manager}
		}
		c := exec.Command("tmux", args...)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	}
	return dashboard(root, reg, *duo)
}
func dashboard(root, path, duo string) error {
	r, e := load(path)
	if e != nil {
		return fmt.Errorf("读取注册表: %w", e)
	}
	s, e := tcell.NewScreen()
	if e != nil {
		return e
	}
	if e = s.Init(); e != nil {
		return e
	}
	defer s.Fini()
	events := s.EventQ()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	selected := 0
	mode := ""
	input := ""
	message := ""
	draw := func() {
		s.Clear()
		_, h := s.Size()
		line := func(y int, text string, style tcell.Style) {
			if y >= h {
				return
			}
			s.PutStrStyled(0, y, text, style)
		}
		normal := tcell.StyleDefault
		title := normal.Foreground(tcell.ColorAqua).Bold(true)
		line(0, "xduo · Duo 实例管理", title)
		line(1, "↑↓ 选择  Enter 打开  n 新建  x 关闭  r 刷新  q 退出", normal)
		line(2, "返回面板：tmux 前缀后按 s，选择 xduo-manager", normal)
		start := 0
		visible := (h - 6) / 3
		if visible < 1 {
			visible = 1
		}
		if selected >= visible {
			start = selected - visible + 1
		}
		for n, i := range r.Items {
			if n < start {
				continue
			}
			y := 4 + (n-start)*3
			if y >= h-2 {
				break
			}
			st, ok := readStatus(i)
			live := alive(i.Tmux)
			state := taskState(st, ok, live, time.Now())
			style := normal
			if n == selected {
				style = normal.Reverse(true)
			}
			remoteState := "未配置"
			if _, ok := remote.LivePairing(i.Pairing, time.Now()); ok {
				remoteState = "已配置"
			}
			update := "—"
			if ok {
				update = st.Updated.Local().Format("2006-01-02 15:04:05")
			}
			line(y, fmt.Sprintf("%s  [%s]  %s  存活:%t  Remote:%s", filepath.Base(i.Project), state, st.Phase, live, remoteState), style)
			line(y+1, i.Project, style)
			line(y+2, fmt.Sprintf("Austin: %s / %s    Tony: %s / %s    更新: %s", st.Austin.Driver, st.Austin.Model, st.Tony.Driver, st.Tony.Model, update), style)
		}
		if mode == "new" {
			line(h-1, "项目路径: "+input+"  (Enter 确认, Esc 取消)", title)
		} else if mode == "confirm" {
			line(h-1, "任务可能仍在运行，关闭？ y 确认 / n 取消", title)
		} else {
			line(h-1, message, title)
		}
		s.Show()
	}
	for {
		draw()
		select {
		case <-tick.C:
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			k, ok := ev.(*tcell.EventKey)
			if !ok {
				continue
			}
			if mode == "new" {
				switch k.Key() {
				case tcell.KeyEsc:
					mode = ""
				case tcell.KeyBackspace, tcell.KeyBackspace2:
					if len(input) > 0 {
						rr := []rune(input)
						input = string(rr[:len(rr)-1])
					}
				case tcell.KeyEnter:
					i, err := create(root, duo, input)
					if err == nil {
						r.Items = append(r.Items, i)
						err = save(path, r)
						if err != nil {
							_ = tmux("kill-session", "-t", "="+i.Tmux)
							r.Items = r.Items[:len(r.Items)-1]
						}
					}
					if err != nil {
						message = err.Error()
					} else {
						selected = len(r.Items) - 1
						message = "实例已启动"
					}
					mode = ""
				case tcell.KeyRune:
					input += k.Str()
				}
				continue
			}
			if mode == "confirm" {
				if k.Str() == "y" && selected < len(r.Items) {
					message = closeInstance(&r, selected, path)
					if selected >= len(r.Items) && selected > 0 {
						selected--
					}
				}
				mode = ""
				continue
			}
			switch k.Key() {
			case tcell.KeyUp:
				if selected > 0 {
					selected--
				}
			case tcell.KeyDown:
				if selected+1 < len(r.Items) {
					selected++
				}
			case tcell.KeyEnter:
				if selected < len(r.Items) {
					i := &r.Items[selected]
					if !alive(i.Tmux) {
						message = "实例已退出"
						break
					}
					i.Activated = time.Now().UTC()
					if err := save(path, r); err != nil {
						message = err.Error()
						break
					}
					if err := tmux("switch-client", "-t", "="+i.Tmux); err != nil {
						message = err.Error()
					}
				}
			case tcell.KeyRune:
				switch k.Str() {
				case "q":
					return nil
				case "r":
					if updated, err := load(path); err == nil {
						r = updated
						if selected >= len(r.Items) && selected > 0 {
							selected = len(r.Items) - 1
						}
					} else {
						message = err.Error()
					}
				case "n":
					mode = "new"
					input = ""
				case "x":
					if selected < len(r.Items) {
						i := r.Items[selected]
						st, ok := readStatus(i)
						if alive(i.Tmux) && taskState(st, ok, true, time.Now()) != "stop" {
							mode = "confirm"
						} else {
							message = closeInstance(&r, selected, path)
							if selected >= len(r.Items) && selected > 0 {
								selected--
							}
						}
					}
				}
			}
		}
	}
}
func closeInstance(r *registry, n int, path string) string {
	i := r.Items[n]
	if alive(i.Tmux) {
		if err := tmux("send-keys", "-t", "="+i.Tmux, "C-q"); err != nil {
			return err.Error()
		}
		deadline := time.Now().Add(3 * time.Second)
		for alive(i.Tmux) && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if alive(i.Tmux) {
			if err := tmux("kill-session", "-t", "="+i.Tmux); err != nil {
				return err.Error()
			}
		}
	}
	old := r.Items
	r.Items = append(append([]instance{}, old[:n]...), old[n+1:]...)
	if err := save(path, *r); err != nil {
		r.Items = old
		return err.Error()
	}
	return "实例已关闭（数据保留）"
}
