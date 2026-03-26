package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"
)

func detectBinary(name, label string) string {
	if p, err := exec.LookPath(name); err == nil {
		printOK("Detected %s: %s", label, p)
		return p
	}
	return name // fallback to just the name
}

func promptWithDefault(r *bufio.Reader, prompt, defaultVal, displayOverride string) string {
	display := defaultVal
	if displayOverride != "" {
		display = displayOverride
	}
	if display != "" {
		fmt.Printf("  %s [\033[36m%s\033[0m]: ", prompt, display)
	} else {
		fmt.Printf("  %s: ", prompt)
	}
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}

func maskToken(token string) string {
	if len(token) < 10 {
		return "****"
	}
	return token[:6] + "..." + token[len(token)-4:]
}

func printLoginStep(n int, title string) {
	fmt.Printf("  \033[1mStep %d/%d: %s\033[0m\n", n, totalLoginSteps, title)
}

func printOK(format string, args ...any) {
	fmt.Printf("  \033[32m✓ %s\033[0m\n", fmt.Sprintf(format, args...))
}

func printFail(format string, args ...any) {
	fmt.Printf("  \033[31m✗ %s\033[0m\n", fmt.Sprintf(format, args...))
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func readCredentials(path string) (*credentialsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var creds credentialsFile
	if err := yaml.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &creds, nil
}

func writeCredentials(path string, creds *credentialsFile) error {
	data, err := yaml.Marshal(creds)
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
