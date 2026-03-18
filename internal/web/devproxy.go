package web

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// DevProxy manages an Angular dev server subprocess and reverse-proxies to it.
type DevProxy struct {
	cmd   *exec.Cmd
	port  int
	proxy *httputil.ReverseProxy
	done  chan struct{}
}

// StartDevProxy finds a free port, starts "pnpm ng serve" in webDir,
// waits for it to become ready, and returns a reverse proxy handler.
func StartDevProxy(webDir string) (*DevProxy, error) {
	absDir, err := filepath.Abs(webDir)
	if err != nil {
		return nil, fmt.Errorf("resolving web directory: %w", err)
	}

	// Auto-install dependencies if needed
	if _, err := os.Stat(filepath.Join(absDir, "node_modules")); os.IsNotExist(err) {
		slog.Info("installing frontend dependencies")
		install := exec.Command("pnpm", "install")
		install.Dir = absDir
		install.Stdout = os.Stdout
		install.Stderr = os.Stderr
		if err := install.Run(); err != nil {
			return nil, fmt.Errorf("pnpm install: %w", err)
		}
	}

	port, err := findFreePort()
	if err != nil {
		return nil, fmt.Errorf("finding free port: %w", err)
	}

	slog.Info("starting angular dev server", "dir", absDir, "port", port)

	cmd := exec.Command("pnpm", "ng", "serve", "--port", fmt.Sprintf("%d", port))
	cmd.Dir = absDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting ng serve: %w", err)
	}

	target := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := waitForServer(target, 120*time.Second); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return nil, fmt.Errorf("angular dev server not ready: %w", err)
	}

	slog.Info("angular dev server ready", "url", target)

	targetURL, _ := url.Parse(target)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(targetURL)
			r.SetXForwarded()
		},
	}

	d := &DevProxy{
		cmd:   cmd,
		port:  port,
		proxy: proxy,
		done:  make(chan struct{}),
	}

	go func() {
		if err := cmd.Wait(); err != nil {
			slog.Warn("angular dev server exited", "error", err)
		}
		close(d.done)
	}()

	return d, nil
}

func (d *DevProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.proxy.ServeHTTP(w, r)
}

func (d *DevProxy) Stop() {
	if d.cmd == nil || d.cmd.Process == nil {
		return
	}
	slog.Info("stopping angular dev server")
	_ = syscall.Kill(-d.cmd.Process.Pid, syscall.SIGINT)
	select {
	case <-d.done:
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(-d.cmd.Process.Pid, syscall.SIGKILL)
		<-d.done
	}
}

func findFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}

func waitForServer(targetURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(targetURL)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout after %v waiting for %s", timeout, targetURL)
}
